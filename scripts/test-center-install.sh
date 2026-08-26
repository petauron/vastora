#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-install-test.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

digest="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
image="ghcr.io/petauron/vastora-center@sha256:$digest"
"$project_dir/scripts/package-center-install.sh" \
  --version 0.1.0-test \
  --image "$image" \
  --output-dir "$temporary_dir/output" >/dev/null

archive="$temporary_dir/output/vastora-center-install.tar.gz"
test -f "$archive"
test -f "$archive.sha256"
test "$(tar -xOzf "$archive" ./release.env | awk -F= '$1 == "VASTORA_VERSION" {print $2; exit}')" = "0.1.0-test"
expected_digest="$(awk 'NR == 1 {print $1}' "$archive.sha256")"
test "$(awk 'NR == 1 {print $2}' "$archive.sha256")" = "vastora-center-install.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  actual_digest="$(sha256sum "$archive" | awk 'NR == 1 {print $1}')"
else
  actual_digest="$(shasum -a 256 "$archive" | awk 'NR == 1 {print $1}')"
fi
test "$expected_digest" = "$actual_digest"
tar -xzf "$archive" -C "$temporary_dir"
grep -Fqx 'VASTORA_VERSION=0.1.0-test' "$temporary_dir/release.env"
grep -Fqx "VASTORA_CENTER_IMAGE=$image" "$temporary_dir/release.env"
test -x "$temporary_dir/setup.sh"
test -x "$temporary_dir/upgrade.sh"
test -x "$temporary_dir/uninstall.sh"
test -x "$temporary_dir/install-update-service.sh"
test -x "$temporary_dir/update-center.sh"
test -f "$temporary_dir/compose.yaml"
grep -Fq 'docker cp "$agent_container:/usr/local/bin/vastora"' "$temporary_dir/upgrade.sh"
grep -Fq 'Co-located Agent updated to $new_version before Center reconciliation.' "$temporary_dir/upgrade.sh"
grep -Fq 'http://127.0.0.1:$bootstrap_port/readyz' "$temporary_dir/upgrade.sh"
grep -Fq 'Co-located Agent reconciled successfully after Center startup.' "$temporary_dir/upgrade.sh"
test ! -e "$temporary_dir/headscale"
if grep -Eq 'headscale/(config\.yaml|policy\.hujson)' "$project_dir/install.sh"; then
  echo "Public installer still requires removed Headscale configuration files" >&2
  exit 1
fi
grep -Fq '127.0.0.1:${VASTORA_CENTER_BOOTSTRAP_PORT:-8080}' "$temporary_dir/compose.yaml"
grep -Fq 'network_mode: host' "$temporary_dir/compose.yaml"
if sed -n '/^  center:/,/^  headscale:/p' "$temporary_dir/compose.yaml" | grep -Fq '    ports:'; then
  echo "Center install bundle still publishes a Docker port" >&2
  exit 1
fi
if sed -n '/^  center:/,/^  deployer:/p' "$temporary_dir/compose.yaml" | grep -Fq '/var/run/docker.sock'; then
  echo "Center service must not mount the Docker socket" >&2
  exit 1
fi
grep -Fq -- '--deployer-socket' "$temporary_dir/compose.yaml"
grep -Fq '/var/run/docker.sock:/var/run/docker.sock' "$temporary_dir/compose.yaml"
grep -Fq '/run/vastora:/run/vastora' "$temporary_dir/compose.yaml"
if grep -Fq '${VASTORA_CENTER_PORT:-443}:8080' "$temporary_dir/compose.yaml"; then
  echo "Center install bundle still claims public port 443" >&2
  exit 1
fi
grep -Fq 'ssh -N -L 18082:127.0.0.1:$bootstrap_port' "$temporary_dir/setup.sh"
grep -Fq 'Public port 443: unchanged' "$temporary_dir/setup.sh"
grep -Fq 'aarch64|arm64)' "$project_dir/install.sh"

fake_bin="$temporary_dir/fake-bin"
existing="$temporary_dir/existing"
install -d "$fake_bin" "$existing"
install -m 0644 "$temporary_dir/compose.yaml" "$existing/compose.yaml"
printf '%s\n' 'old setup' > "$existing/setup.sh"
printf '%s\n' 'VASTORA_VERSION=old' 'VASTORA_CENTER_IMAGE=old-image' > "$existing/release.env"
printf '%s\n' 'VASTORA_CENTER_IMAGE=old-image' 'VASTORA_CENTER_BOOTSTRAP_PORT=19090' 'VASTORA_CUSTOM_VALUE=preserved' > "$existing/.env"
cat > "$fake_bin/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$fake_bin/systemctl" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$fake_bin/docker" "$fake_bin/curl" "$fake_bin/systemctl"
VASTORA_SYSTEMD_UNIT_DIR="$temporary_dir/systemd" PATH="$fake_bin:$PATH" "$temporary_dir/upgrade.sh" --install-dir "$existing" >/dev/null
grep -Fqx "VASTORA_CENTER_IMAGE=$image" "$existing/.env"
grep -Fqx 'VASTORA_CENTER_BOOTSTRAP_PORT=19090' "$existing/.env"
grep -Fqx 'VASTORA_CUSTOM_VALUE=preserved' "$existing/.env"
grep -Fqx 'VASTORA_VERSION=0.1.0-test' "$existing/release.env"
test -x "$existing/upgrade.sh"
test -x "$existing/uninstall.sh"
test -x "$existing/update-center.sh"
test -f "$temporary_dir/systemd/vastora-center-update.service"
test -f "$temporary_dir/systemd/vastora-center-update.path"
grep -Fq "PathExists=$existing/.update-request" "$temporary_dir/systemd/vastora-center-update.path"
grep -Fq "$existing/update-center.sh --install-dir $existing" "$temporary_dir/systemd/vastora-center-update.service"

if "$project_dir/scripts/package-center-install.sh" \
  --version 0.1.0-test \
  --image 'ghcr.io/petauron/vastora-center@sha256:not-a-digest' \
  --output-dir "$temporary_dir/invalid" >/dev/null 2>&1; then
  echo "Invalid image digest was accepted" >&2
  exit 1
fi

echo "Center install bundle test passed"
