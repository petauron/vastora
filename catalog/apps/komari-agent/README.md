# Komari Agent

Vastora installs Komari Agent as a native systemd service rather than a Docker
container. The signed catalog selects the exact `linux/amd64` or `linux/arm64`
release asset and the Agent verifies its SHA-256 before installation.

Managed paths:

- `/opt/komari/agent`
- `/etc/komari-agent/config.json` (`0600`)
- `/etc/systemd/system/komari-agent.service`

The service runs `/opt/komari/agent --config
/etc/komari-agent/config.json`. Vastora disables upstream self-update and Web
SSH so upgrades and remote-control policy remain under Center. Reconfiguration
atomically replaces the binary, JSON and unit, verifies the restarted service,
and restores the previous files if verification fails.

The former `vastora-komari-agent` container is not a supported runtime. A
runtime-generation migration removes it only after the native service is
healthy.
