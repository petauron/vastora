#!/bin/sh
set -eu

usage() {
  echo 'usage: verify-vps-onboarding.sh [-F SSH_CONFIG] SSH_ALIAS' >&2
  exit 2
}

config=
if [ "${1-}" = '-F' ]; then
  [ "$#" -ge 3 ] || usage
  config=$2
  shift 2
fi
[ "$#" -eq 1 ] || usage
case "$1" in
  ''|-*|*[!a-zA-Z0-9._-]*) usage ;;
esac
target=$1

# The remote shell receives the actual authenticated connection's account and
# internal listener port. Force a fresh connection and retain strict host keys.
if [ -n "$config" ]; then
  set -- -F "$config"
else
  set --
fi
exec ssh "$@" -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o UpdateHostKeys=no -o ControlMaster=no -o ControlPath=none -o ConnectTimeout=15 \
  -o PreferredAuthentications=publickey -o PubkeyAuthentication=yes \
  -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no \
  -o GSSAPIAuthentication=no -o HostbasedAuthentication=no \
  "$target" 'sh -s' <<'REMOTE'
set -eu
[ -n "${SSH_CONNECTION-}" ] || { echo 'SSH connection context unavailable' >&2; exit 1; }
set -f
set -- $SSH_CONNECTION
[ "$#" -eq 4 ] || { echo 'SSH connection context invalid' >&2; exit 1; }
case "$4" in ''|*[!0-9]*) echo 'SSH listener port invalid' >&2; exit 1 ;; esac
user=$(id -un)
exec sudo -n /usr/local/bin/vastora agent verify-onboarding --ssh-user "$user" --ssh-port "$4"
REMOTE
