#!/usr/bin/env python3
"""Host-owned restic runner. Never runs inside the services it snapshots."""
import argparse
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import shutil
import socket
import signal
import shlex
import time
import subprocess
import tempfile


def save(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def read(path, default):
    return json.loads(path.read_text()) if path.exists() else default


def command(*args, timeout=120, env=None):
    # Do not copy subprocess output into public status: inspect can contain secrets.
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=env)
    if result.returncode:
        raise RuntimeError(f'{args[0]} {args[1]} failed (exit {result.returncode}); inspect the host journal')
    return result.stdout


def overlaps(a, b):
    return a == b or a in b.parents or b in a.parents


def managed(container):
    labels = container.get('Config', {}).get('Labels') or {}
    return labels.get('io.vastora.managed') == 'true'


def inventory(install, config):
    names = config.get('containers', [])
    if not names or any(not isinstance(name, str) or name.startswith('-') or '/' in name for name in names):
        raise ValueError('A reviewed Vastora-managed container list is required')
    containers = json.loads(command('docker', 'inspect', *names))
    if any(not managed(container) for container in containers):
        raise ValueError('Backup scope contains a container not managed by Vastora')
    # The installer directory and owned mounts are sufficient; never sweep /etc
    # or Compose project directories that can also contain unrelated services.
    sources = {install.resolve()}
    for container in containers:
        for mount in container.get('Mounts', []):
            source = Path(mount['Source'])
            if str(source).startswith(('/run/', '/var/run/', '/dev/', '/proc/', '/sys/')):
                continue
            if source.is_file() or source.is_dir():
                sources.add(source.resolve())
    active = []
    if config.get('includeAgent', True):
        unit = Path('/etc/systemd/system/vastora-agent.service')
        if unit.is_file():
            lines = unit.read_text().splitlines()
            if 'Description=Vastora Agent' not in lines:
                raise ValueError('Local Agent unit ownership could not be verified')
            starts = [line[len('ExecStart='):] for line in lines if line.startswith('ExecStart=')]
            if len(starts) != 1:
                raise ValueError('Local Agent command is ambiguous')
            args = shlex.split(starts[0])
            if args[1:3] != ['agent', 'serve'] or args.count('--data-dir') != 1:
                raise ValueError('Local Agent data directory could not be verified')
            index = args.index('--data-dir') + 1
            if index >= len(args):
                raise ValueError('Local Agent data directory is missing')
            data = Path(args[index])
            if not data.is_absolute() or not data.is_dir():
                raise ValueError('Local Agent data directory is unavailable')
            sources.update((unit, data.resolve()))
            overrides = Path(str(unit) + '.d')
            if overrides.exists():
                raise ValueError('Review Agent unit overrides before enabling Agent backups')
            if subprocess.run(['systemctl', 'is-active', '--quiet', unit.name], check=False).returncode == 0:
                active.append(unit.name)
    for source in sources:
        if str(source) in ('/', '/etc', '/var', '/var/lib', '/var/lib/docker', '/opt', '/root', '/home'):
            raise ValueError('Inventory contains an overly broad source directory')
    # Even an owned container may share storage with an independent application.
    # Fail before stopping anything instead of silently copying another service.
    ids = command('docker', 'ps', '-aq').split()
    all_containers = json.loads(command('docker', 'inspect', *ids)) if ids else []
    selected_ids = {container['Id'] for container in containers}
    for other in all_containers:
        if other['Id'] in selected_ids:
            continue
        for mount in other.get('Mounts', []):
            shared = Path(mount['Source']).resolve()
            if any(overlaps(shared, source) for source in sources):
                raise ValueError('Backup storage overlaps a container outside the reviewed scope')
    return containers, sorted(str(p) for p in sources), active


def recover(state):
    journal = read(state / 'restart.json', {'containers': [], 'units': []})
    failures = []
    for container in reversed(journal['containers']):
        try:
            command('docker', 'start', container)
            deadline = time.monotonic() + 120
            while True:
                running = json.loads(command('docker', 'inspect', container))[0]['State']
                if running.get('Running') and running.get('Health', {}).get('Status', 'healthy') == 'healthy':
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError('Container did not recover')
                time.sleep(2)
        except Exception:
            failures.append(container)
    for unit in reversed(journal['units']):
        try:
            command('systemctl', 'start', unit)
            command('systemctl', 'is-active', '--quiet', unit)
        except Exception:
            failures.append(unit)
    if failures:
        raise RuntimeError('Service recovery incomplete; restart journal retained')
    (state / 'restart.json').unlink(missing_ok=True)


def run(install):
    state = install / '.host-backup'
    state.mkdir(mode=0o700, exist_ok=True)
    with (state / 'lock').open('w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        # Recovery always precedes configuration validation and scheduling.
        interrupted = (state / 'restart.json').exists()
        recover(state)
        if interrupted:
            save(state / 'status.json', {'state': 'failed', 'message': 'Interrupted backup; services restarted.'})
        config = read(state / 'config.json', {})
        scope = read(state / 'scope.json', {})
        if config and scope.get('reviewed') is not True:
            raise ValueError('Review Vastora-managed service scope before enabling backups')
        if 'sources' in scope or 'units' in scope:
            raise ValueError('Arbitrary host sources and services are not supported')
        config.update({key: scope.get(key, []) for key in ('containers', 'excludes')})
        config['includeAgent'] = scope.get('includeAgent', True)
        if 'directory' not in config:
            return
        containers, sources, units = inventory(install, config)
        save(state / 'coverage.json', {'containers': [c['Name'].lstrip('/') for c in containers], 'sources': sources, 'units': units})
        now = dt.datetime.now(dt.timezone.utc)
        requested = (state / 'request').exists()
        last = read(state / 'last-attempt.json', {}).get('at')
        due = now.strftime('%H:%M') >= config['timeUTC'] and (
            not last or (now.date() - dt.datetime.fromisoformat(last).date()).days >= config['intervalDays'])
        if not requested and not (config['enabled'] and due):
            return
        if (install / '.update-request').exists() or read(install / '.update-status.json', {}).get('state') in ('queued', 'applying'):
            return
        (state / 'request').unlink(missing_ok=True)
        save(state / 'last-attempt.json', {'at': now.isoformat()})
        try:
            backup(install, state, config, now)
        except Exception as error:
            save(state / 'status.json', {'state': 'failed', 'at': now.isoformat(), 'message': str(error)})
            raise


def backup(install, state, config, now):
    if not shutil.which('restic'):
        raise RuntimeError('Install restic on the host before enabling backups')
    password = state / 'password'
    if not password.is_file() or password.stat().st_mode & 0o077:
        raise ValueError('Backup password file must exist with owner-only permissions')
    containers, sources, units = inventory(install, config)
    repository = Path(config['directory'])
    if not repository.is_absolute() or repository.resolve() != repository:
        raise ValueError('Repository must be an absolute path without symlinks or traversal')
    if any(overlaps(repository, Path(source)) for source in sources):
        raise ValueError('Repository and source directories overlap')
    if any(overlaps(repository, Path(root)) for root in ('/etc', '/run', '/proc', '/sys', '/dev', '/var/lib/docker')):
        raise ValueError('Repository overlaps protected host storage')
    env = dict(os.environ, RESTIC_REPOSITORY=str(repository), RESTIC_PASSWORD_FILE=str(password),
               RESTIC_CACHE_DIR=str(state / 'cache'))
    if not (repository / 'config').exists():
        if repository.exists() and any(repository.iterdir()):
            raise ValueError('New repository directory must be empty')
        command('restic', 'init', env=env)
        save(repository / '.vastora-host-repository', {'host': socket.gethostname()})
    if read(repository / '.vastora-host-repository', {}).get('host') != socket.gethostname():
        raise ValueError('Repository is not owned by this host backup mechanism')
    # Store definitions only inside the encrypted snapshot; never expose Config.Env.
    manifest = state / 'manifest.json'
    save(manifest, {'containers': containers, 'units': units, 'sources': sources, 'at': now.isoformat()})
    save(state / 'status.json', {'state': 'running', 'at': now.isoformat(), 'message': 'Creating consistent Vastora service snapshot'})
    journal = {'containers': [], 'units': []}
    summary = None
    try:
        for unit in units:
            journal['units'].append(unit)
            save(state / 'restart.json', journal)
            command('systemctl', 'stop', unit)
        for container in containers:
            if not container['State']['Running']:
                continue
            journal['containers'].append(container['Id'])
            save(state / 'restart.json', journal)
            command('docker', 'stop', '--time', '60', container['Id'])
        stopped = json.loads(command('docker', 'inspect', *[c['Id'] for c in containers]))
        if any(c['State']['Running'] or c['State'].get('ExitCode') == 137 for c in stopped):
            raise RuntimeError('A container did not stop cleanly; snapshot refused')
        exclusions = [str(state / child) for child in ('cache', 'password', 'lock', 'restart.json')]
        exclusions.extend(config.get('excludes', []))
        arguments = ['restic', 'backup', '--json', '--tag', 'vastora-host', '--group-by', 'host,tags']
        for excluded in exclusions:
            arguments.extend(['--exclude', excluded])
        output = command(*arguments, '--', *sources, timeout=7200, env=env)
        for line in output.splitlines():
            value = json.loads(line)
            if value.get('message_type') == 'summary':
                summary = value
        if not summary or not summary.get('snapshot_id'):
            raise RuntimeError('Backup did not produce a complete snapshot')
    finally:
        recover(state)
        manifest.unlink(missing_ok=True)
    # Services are back before repository maintenance. Never prune after failed backup.
    command('restic', 'check', timeout=3600, env=env)
    command('restic', 'forget', '--tag', 'vastora-host', '--group-by', 'host,tags',
            '--host', socket.gethostname(), '--keep-last', str(config['keepLast']), '--prune', timeout=7200, env=env)
    snapshots = json.loads(command('restic', 'snapshots', '--json', '--tag', 'vastora-host', '--host', socket.gethostname(), env=env))
    save(state / 'snapshots.json', [{'id': s['id'], 'time': s['time'],
         'bytes': s.get('summary', {}).get('total_bytes_processed', 0)} for s in snapshots])
    save(state / 'status.json', {'state': 'success', 'at': now.isoformat(),
         'snapshotId': summary['snapshot_id'], 'bytes': summary.get('total_bytes_processed', 0),
         'message': 'Local snapshot created; offsite copy and restore verification not performed.'})


if __name__ == '__main__':
    def terminate(_signal, _frame):
        raise RuntimeError('Backup interrupted')
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    parser = argparse.ArgumentParser()
    parser.add_argument('--install-dir', required=True, type=Path)
    options = parser.parse_args()
    try:
        run(options.install_dir)
    except Exception as error:
        save(options.install_dir / '.host-backup' / 'status.json',
             {'state': 'failed', 'at': dt.datetime.now(dt.timezone.utc).isoformat(), 'message': str(error)})
        raise
