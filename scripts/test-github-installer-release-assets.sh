#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-github-assets-test.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

fake_bin="$temporary_dir/bin"
fake_release="$temporary_dir/release"
source_dir="$temporary_dir/dist"
bundle_dir="$temporary_dir/bundle"
mkdir -p "$fake_bin" "$fake_release" "$source_dir" "$bundle_dir"

version="0.1.0-alpha.10"
tag="v$version"
printf 'VASTORA_VERSION=%s\n' "$version" > "$bundle_dir/release.env"
tar -czf "$source_dir/vastora-center-install.tar.gz" -C "$bundle_dir" .
(cd "$source_dir" && sha256sum vastora-center-install.tar.gz > vastora-center-install.tar.gz.sha256)
cp "$project_dir/install.sh" "$source_dir/install.sh"
"$script_dir/create-installer-release-manifest.sh" --version "$version" --source-dir "$source_dir" >/dev/null

cat > "$fake_bin/gh" <<'EOF'
#!/bin/sh
set -eu
[ "${1:-}" = "release" ] || exit 2
operation="${2:-}"
shift 2
case "$operation" in
  view)
    if [ -f "$FAKE_RELEASE_DIR/published" ]; then draft=false; else draft=true; fi
    find "$FAKE_RELEASE_DIR" -maxdepth 1 -type f ! -name published ! -name names -exec basename {} \; | sort > "$FAKE_RELEASE_DIR/names"
    jq -R -s --argjson draft "$draft" '{isDraft:$draft,assets:(split("\n") | map(select(length > 0) | {name:.}))}' "$FAKE_RELEASE_DIR/names"
    ;;
  upload)
    shift
    for argument in "$@"; do
      [ "$argument" = "--clobber" ] && continue
      cp "$argument" "$FAKE_RELEASE_DIR/$(basename "$argument")"
    done
    ;;
  download)
    shift
    output=""
    while [ "$#" -gt 0 ]; do
      case "$1" in --dir) output="$2"; shift 2 ;; *) shift ;; esac
    done
    mkdir -p "$output"
    find "$FAKE_RELEASE_DIR" -maxdepth 1 -type f ! -name published ! -name names -exec cp {} "$output/" \;
    ;;
  *) exit 2 ;;
esac
EOF
chmod 0755 "$fake_bin/gh"
export FAKE_RELEASE_DIR="$fake_release"
export PATH="$fake_bin:$PATH"

"$script_dir/github-installer-release-assets.sh" upload --tag "$tag" --directory "$source_dir" >/dev/null
test "$(find "$fake_release" -maxdepth 1 -type f ! -name names | wc -l | tr -d ' ')" = "4"

retry_dir="$temporary_dir/retry"
"$script_dir/github-installer-release-assets.sh" download --tag "$tag" --directory "$retry_dir" >/dev/null
for asset in install.sh vastora-center-install.tar.gz vastora-center-install.tar.gz.sha256 vastora-release-manifest.json; do
  cmp -s "$source_dir/$asset" "$retry_dir/$asset"
done

printf 'unexpected\n' > "$fake_release/unexpected.txt"
if "$script_dir/github-installer-release-assets.sh" upload --tag "$tag" --directory "$source_dir" >/dev/null 2>&1; then
  echo "GitHub asset publication accepted an unexpected release asset." >&2
  exit 1
fi
rm "$fake_release/unexpected.txt"

touch "$fake_release/published"
if "$script_dir/github-installer-release-assets.sh" upload --tag "$tag" --directory "$source_dir" >/dev/null 2>&1; then
  echo "GitHub asset publication mutated a published release." >&2
  exit 1
fi

echo "GitHub installer release asset tests: OK"
