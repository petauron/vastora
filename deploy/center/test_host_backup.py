"""Isolated runner tests; no Docker or host-service operations are performed."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('host_backup', Path(__file__).with_name('host-backup.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class HostBackupTests(unittest.TestCase):
    def test_failed_restart_retains_journal(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            runner.save(state / 'restart.json', {'containers': ['fixture'], 'units': []})
            with patch.object(runner, 'command', side_effect=RuntimeError('fixture failure')):
                with self.assertRaises(RuntimeError):
                    runner.recover(state)
            self.assertTrue((state / 'restart.json').exists())

    def test_restart_uses_reverse_order_and_only_recorded_services(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            runner.save(state / 'restart.json', {'containers': ['first', 'second'], 'units': ['native.service']})
            calls = []
            def command(*args, **kwargs):
                calls.append(args)
                return json.dumps([{'State': {'Running': True}}]) if args[:2] == ('docker', 'inspect') else ''
            with patch.object(runner, 'command', side_effect=command):
                runner.recover(state)
            self.assertEqual([c for c in calls if c[:2] == ('docker', 'start')], [('docker', 'start', 'second'), ('docker', 'start', 'first')])
            self.assertFalse((state / 'restart.json').exists())

    def test_failed_snapshot_restarts_services_without_pruning(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / 'install' / '.host-backup'
            state.mkdir(parents=True)
            (state / 'password').write_text('fixture-only-password')
            (state / 'password').chmod(0o600)
            repository = root / 'repository'
            repository.mkdir()
            (repository / 'config').write_text('{}')
            runner.save(repository / '.vastora-host-repository', {'host': runner.socket.gethostname()})
            def command(*args, **kwargs):
                if args[:2] == ('restic', 'backup'):
                    raise RuntimeError('fixture partial read failure')
                if args[:2] == ('docker', 'inspect'):
                    return json.dumps([{'State': {'Running': False, 'ExitCode': 0}}])
                return ''
            container = {'Id': 'fixture', 'State': {'Running': True}}
            with patch.object(runner.shutil, 'which', return_value='/bin/restic'), \
                 patch.object(runner, 'inventory', return_value=([container], ['/fixture-data'], [])), \
                 patch.object(runner, 'command', side_effect=command) as calls, \
                 patch.object(runner, 'recover') as recover:
                with self.assertRaises(RuntimeError):
                    runner.backup(root / 'install', state, {'directory': str(repository), 'keepLast': 7}, runner.dt.datetime.now(runner.dt.timezone.utc))
                recover.assert_called_once_with(state)
                self.assertFalse(any(call.args[:2] == ('restic', 'forget') for call in calls.call_args_list))

    def test_overlap_is_symmetric(self):
        self.assertTrue(runner.overlaps(Path('/srv/data'), Path('/srv/data/backup')))
        self.assertTrue(runner.overlaps(Path('/srv/data/backup'), Path('/srv/data')))
        self.assertFalse(runner.overlaps(Path('/srv/data'), Path('/srv/backups')))

    def test_independent_container_is_rejected_before_inventory_or_stopping(self):
        independent = {'Id': 'external', 'Config': {'Labels': {}}, 'Mounts': []}
        with patch.object(runner, 'command', return_value=json.dumps([independent])) as calls:
            with self.assertRaisesRegex(ValueError, 'not managed by Vastora'):
                runner.inventory(Path('/fixture'), {'containers': ['independent-app']})
            calls.assert_called_once_with('docker', 'inspect', 'independent-app')

    def test_scope_uses_owned_mounts_not_etc_or_compose_working_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            install = Path(directory) / 'center'
            data = Path(directory) / 'app-data'
            data.mkdir()
            container = {'Id': 'owned', 'Config': {'Labels': {
                'io.vastora.managed': 'true',
                'com.docker.compose.project.working_dir': '/independent-project'}},
                'Mounts': [{'Source': str(data)}]}
            with patch.object(runner, 'command', side_effect=[json.dumps([container]), 'owned', json.dumps([container])]):
                _, sources, units = runner.inventory(install, {'containers': ['owned'], 'includeAgent': False})
            self.assertEqual(set(sources), {str(install.resolve()), str(data.resolve())})
            self.assertEqual(units, [])

    def test_shared_mount_with_independent_service_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            data = Path(directory) / 'shared'
            data.mkdir()
            owned = {'Id': 'owned', 'Config': {'Labels': {'io.vastora.managed': 'true'}}, 'Mounts': [{'Source': str(data)}]}
            independent = {'Id': 'external', 'Mounts': [{'Source': str(data)}]}
            with patch.object(runner, 'command', side_effect=[json.dumps([owned]), 'owned external', json.dumps([owned, independent])]):
                with self.assertRaisesRegex(ValueError, 'outside the reviewed scope'):
                    runner.inventory(Path(directory) / 'center', {'containers': ['owned'], 'includeAgent': False})

    def test_name_does_not_establish_ownership(self):
        self.assertFalse(runner.managed({'Name': '/vastora-independent', 'Config': {'Labels': {}}}))
        self.assertTrue(runner.managed({'Config': {'Labels': {'io.vastora.managed': 'true'}}}))

    def test_unreviewed_container_inventory_is_rejected(self):
        with self.assertRaises(ValueError):
            runner.inventory(Path('/fixture'), {})


if __name__ == '__main__':
    unittest.main()
