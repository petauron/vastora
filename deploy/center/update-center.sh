#!/bin/sh
set -eu

install_dir="/opt/vastora/center"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir) install_dir="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$install_dir" in
  /*) ;;
  *) echo "The Center installation directory must be absolute." >&2; exit 2 ;;
esac
if [ "$(id -u)" -ne 0 ]; then
  echo "Center updates must run as root." >&2
  exit 1
fi

request="$install_dir/.update-request"
status_file="$install_dir/.update-status.json"
if [ ! -f "$request" ]; then
  exit 0
fi

target_version="$(awk 'NR == 1 {print; exit}' "$request")"
rm -f "$request"
if ! printf '%s\n' "$target_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "The requested Center version is invalid." >&2
  exit 2
fi

write_status() {
  update_state="$1"
  update_version="$2"
  update_message="$3"
  update_time="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  temporary_status="$(mktemp "$install_dir/.update-status.XXXXXX")"
  chmod 0600 "$temporary_status"
  printf '{"state":"%s","targetVersion":"%s","message":"%s","updatedAt":"%s"}\n' \
    "$update_state" "$update_version" "$update_message" "$update_time" > "$temporary_status"
  mv "$temporary_status" "$status_file"
}

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-update.XXXXXX")"
installer="$temporary_dir/install.sh"
completed=no
cleanup() {
  result=$?
  rm -rf "$temporary_dir"
  if [ "$result" -ne 0 ] && [ "$completed" = no ]; then
    write_status failed "$target_version" "Update failed. Review the host service log, then retry."
  fi
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

write_status applying "$target_version" "Downloading and applying the verified release."
release_base="https://github.com/petauron/vastora/releases/download/v$target_version"
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL \
  "$release_base/install.sh" -o "$installer"
chmod 0700 "$installer"
/bin/sh "$installer" center \
  --release-url "$release_base/vastora-center-install.tar.gz" \
  --install-dir "$install_dir"

installed_version="$(awk -F= '$1 == "VASTORA_VERSION" {sub(/^[^=]*=/, ""); print; exit}' "$install_dir/release.env")"
if [ -z "$installed_version" ]; then installed_version="$target_version"; fi
write_status succeeded "$installed_version" "Center was updated successfully."
completed=yes
