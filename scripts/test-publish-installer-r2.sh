#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-r2-test.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

fake_bin="$temporary_dir/bin"
fake_r2="$temporary_dir/r2"
source_dir="$temporary_dir/dist"
mkdir -p "$fake_bin" "$fake_r2/objects" "$source_dir"
printf 'bundle\n' > "$source_dir/vastora-center-install.tar.gz"
(cd "$source_dir" && sha256sum vastora-center-install.tar.gz > vastora-center-install.tar.gz.sha256)

cat > "$fake_bin/aws" <<'EOF'
#!/bin/sh
set -eu
while [ "$#" -gt 0 ] && [ "$1" != "s3api" ]; do shift; done
[ "${1:-}" = "s3api" ] || exit 2
shift
operation="${1:-}"
shift
key=""
body=""
metadata=""
query=""
destination=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --key) key="$2"; shift 2 ;;
    --body) body="$2"; shift 2 ;;
    --metadata) metadata="$2"; shift 2 ;;
    --query) query="$2"; shift 2 ;;
    --bucket|--content-type|--cache-control|--endpoint-url|--output) shift 2 ;;
    --no-cli-pager) shift ;;
    *) destination="$1"; shift ;;
  esac
done
object="$FAKE_R2_DIR/objects/$key"
meta="$FAKE_R2_DIR/metadata/$key"
case "$operation" in
  head-object)
    [ -f "$object" ] || exit 254
    digest="$(cat "$meta")"
    if [ -n "$query" ]; then
      printf '%s\n' "$digest"
    else
      printf '{"Metadata":{"sha256":"%s"}}\n' "$digest"
    fi
    ;;
  put-object)
    mkdir -p "$(dirname "$object")" "$(dirname "$meta")"
    cp "$body" "$object"
    digest="$(printf '%s' "$metadata" | sed -n 's/^sha256=\([^,]*\).*$/\1/p')"
    printf '%s' "$digest" > "$meta"
    printf '{}\n'
    ;;
  get-object)
    [ -f "$object" ] || exit 254
    cp "$object" "$destination"
    printf '{}\n'
    ;;
  *) exit 2 ;;
esac
EOF
chmod 0755 "$fake_bin/aws"

export FAKE_R2_DIR="$fake_r2"
export PATH="$fake_bin:$PATH"
endpoint="https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
bucket="petauron-downloads"
version="0.1.0-test"

"$script_dir/publish-installer-r2.sh" stage --version "$version" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" --installer "$project_dir/install.sh" >/dev/null
manifest="$fake_r2/objects/vastora/releases/v$version/manifest.json"
test -f "$manifest"
jq -e --arg version "$version" '.schema == 1 and .version == $version and (.assets | length) == 3' "$manifest" >/dev/null

"$script_dir/publish-installer-r2.sh" activate --version "$version" --bucket "$bucket" --endpoint "$endpoint" >/dev/null
cmp -s "$manifest" "$fake_r2/objects/vastora/current.json"

printf 'changed bundle\n' > "$source_dir/vastora-center-install.tar.gz"
if "$script_dir/publish-installer-r2.sh" stage --version "$version" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" --installer "$project_dir/install.sh" >/dev/null 2>&1; then
  echo "Immutable R2 release assets accepted changed content." >&2
  exit 1
fi

echo "R2 installer publication tests: OK"
