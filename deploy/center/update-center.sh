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
for required in awk curl grep mktemp sha256sum; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done

request="$install_dir/.update-request"
status_file="$install_dir/.update-status.json"
if [ ! -f "$request" ]; then
  exit 0
fi

target_version="$(awk 'NR == 1 {print; exit}' "$request")"
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

installed_version="$(awk -F= '$1 == "VASTORA_VERSION" {sub(/^[^=]*=/, ""); print; exit}' "$install_dir/release.env")"
if [ "$installed_version" = "$target_version" ]; then
  write_status succeeded "$installed_version" "Center was updated successfully."
  rm -f "$request"
  exit 0
fi

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-update.XXXXXX")"
installer="$temporary_dir/install.sh"
installer_headers="$temporary_dir/install.headers"
completed=no
failure_message="Update failed. Review the host service log, then retry."
cleanup() {
  result=$?
  rm -rf "$temporary_dir"
  if [ "$result" -ne 0 ] && [ "$completed" = no ]; then
    write_status failed "$target_version" "$failure_message"
    rm -f "$request"
  fi
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

write_status applying "$target_version" "Downloading the verified release metadata."
release_base="https://vastora.petauron.com/releases/v$target_version"
failure_message="The immutable Center installer could not be downloaded."
if ! curl --proto '=https' --tlsv1.2 -fsS \
  --dump-header "$installer_headers" \
  "$release_base/install.sh" -o "$installer"; then
  exit 1
fi
write_status applying "$target_version" "Verifying the immutable release."
installer_version="$(awk '
  index($0, ":") > 0 && tolower(substr($0, 1, index($0, ":") - 1)) == "x-vastora-version" {
    value = substr($0, index($0, ":") + 1)
    sub(/^[[:space:]]+/, "", value)
    sub(/\r$/, "", value)
    result = value
  }
  END { print result }
' "$installer_headers")"
if [ "$installer_version" != "$target_version" ]; then
  failure_message="The immutable Center installer version did not match the requested update."
  exit 1
fi
expected_installer_digest="$(awk '
  index($0, ":") > 0 && tolower(substr($0, 1, index($0, ":") - 1)) == "x-vastora-sha256" {
    value = substr($0, index($0, ":") + 1)
    sub(/^[[:space:]]+/, "", value)
    sub(/\r$/, "", value)
    result = tolower(value)
  }
  END { print result }
' "$installer_headers")"
if ! printf '%s\n' "$expected_installer_digest" | grep -Eq '^[0-9a-f]{64}$' || \
   [ "$(sha256sum "$installer" | awk 'NR == 1 {print tolower($1)}')" != "$expected_installer_digest" ]; then
  failure_message="The immutable Center installer failed its SHA-256 integrity check."
  exit 1
fi
chmod 0700 "$installer"
write_status applying "$target_version" "Installing the verified release."
failure_message="The verified Center installer did not complete successfully."
if ! VASTORA_UPDATE_STATUS_FILE="$status_file" \
  VASTORA_UPDATE_TARGET_VERSION="$target_version" \
  /bin/sh "$installer" center \
  --release-url "$release_base/vastora-center-install.tar.gz" \
  --install-dir "$install_dir" \
  --expected-version "$target_version"; then
  exit 1
fi

installed_version="$(awk -F= '$1 == "VASTORA_VERSION" {sub(/^[^=]*=/, ""); print; exit}' "$install_dir/release.env")"
if [ "$installed_version" != "$target_version" ]; then
  failure_message="The Center update completed with an unexpected installed version."
  exit 1
fi
write_status succeeded "$installed_version" "Center was updated successfully."
rm -f "$request"
completed=yes
