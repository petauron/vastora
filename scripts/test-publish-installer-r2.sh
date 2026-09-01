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
bundle_dir="$temporary_dir/bundle"
mkdir -p "$fake_bin" "$fake_r2/objects" "$source_dir" "$bundle_dir"

create_bundle() {
  bundle_version="$1"
  bundle_content="$2"
  printf 'VASTORA_VERSION=%s\n' "$bundle_version" > "$bundle_dir/release.env"
  printf '%s\n' "$bundle_content" > "$bundle_dir/content.txt"
  tar -czf "$source_dir/vastora-center-install.tar.gz" -C "$bundle_dir" .
  (cd "$source_dir" && sha256sum vastora-center-install.tar.gz > vastora-center-install.tar.gz.sha256)
}

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
prefix=""
continuation=""
delete_request=""
max_keys="1000"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --key) key="$2"; shift 2 ;;
    --body) body="$2"; shift 2 ;;
    --metadata) metadata="$2"; shift 2 ;;
    --query) query="$2"; shift 2 ;;
    --prefix) prefix="$2"; shift 2 ;;
    --continuation-token) continuation="$2"; shift 2 ;;
    --delete) delete_request="$2"; shift 2 ;;
    --max-keys) max_keys="$2"; shift 2 ;;
    --bucket|--content-type|--cache-control|--endpoint-url|--output) shift 2 ;;
    --no-cli-pager) shift ;;
    *) destination="$1"; shift ;;
  esac
done
object="$FAKE_R2_DIR/objects/$key"
meta="$FAKE_R2_DIR/metadata/$key"
case "$operation" in
  head-object)
    if [ ! -f "$object" ]; then
      echo 'An error occurred (404) when calling the HeadObject operation: Not Found' >&2
      exit 254
    fi
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
    if [ "$key" = "vastora/current.json" ] && [ -f "$FAKE_R2_DIR/change-pointer" ]; then
      count_file="$FAKE_R2_DIR/current-read-count"
      count=0
      [ ! -f "$count_file" ] || count="$(cat "$count_file")"
      count=$((count + 1))
      printf '%s\n' "$count" > "$count_file"
      if [ "$count" -ge 2 ]; then
        printf '{"schema":0}\n' > "$destination"
      else
        cp "$object" "$destination"
      fi
    else
      cp "$object" "$destination"
    fi
    printf '{}\n'
    ;;
  list-objects-v2)
    keys_file="$FAKE_R2_DIR/list-keys"
    find "$FAKE_R2_DIR/objects" -type f | sed "s|^$FAKE_R2_DIR/objects/||" | sort |
      awk -v prefix="$prefix" 'index($0, prefix) == 1' > "$keys_file"
    start="${continuation:-0}"
    total="$(wc -l < "$keys_file" | tr -d ' ')"
    page_file="$FAKE_R2_DIR/list-page"
    tail -n "+$((start + 1))" "$keys_file" | head -n "$max_keys" > "$page_file"
    count="$(wc -l < "$page_file" | tr -d ' ')"
    next=$((start + count))
    if [ "$next" -lt "$total" ]; then truncated=true; else truncated=false; fi
    jq -R -s --argjson truncated "$truncated" --arg token "$next" '{IsTruncated:$truncated, NextContinuationToken:(if $truncated then $token else null end), Contents:(split("\n") | map(select(length > 0) | {Key:.}))}' "$page_file"
    ;;
  delete-objects)
    request="${delete_request#file://}"
    if [ -f "$FAKE_R2_DIR/fail-delete" ]; then
      failed_key="$(jq -r '.Objects[0].Key' "$request")"
      jq -n --arg key "$failed_key" '{Errors:[{Key:$key,Code:"AccessDenied",Message:"denied"}]}'
      exit 0
    fi
    skip_key=""
    if [ -f "$FAKE_R2_DIR/skip-delete" ]; then skip_key="$(jq -r '.Objects[0].Key' "$request")"; fi
    jq -r '.Objects[].Key' "$request" | while IFS= read -r delete_key; do
      [ "$delete_key" = "$skip_key" ] && continue
      rm -f "$FAKE_R2_DIR/objects/$delete_key" "$FAKE_R2_DIR/metadata/$delete_key"
    done
    jq '{Deleted:.Objects}' "$request"
    ;;
  *) exit 2 ;;
esac
EOF
chmod 0755 "$fake_bin/aws"

export FAKE_R2_DIR="$fake_r2"
export PATH="$fake_bin:$PATH"
endpoint="https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
bucket="petauron-downloads"
version="0.1.0-alpha.10"
create_bundle "$version" initial
cp "$project_dir/install.sh" "$source_dir/install.sh"
"$script_dir/create-installer-release-manifest.sh" --version "$version" --source-dir "$source_dir" >/dev/null

"$script_dir/publish-installer-r2.sh" stage --version "$version" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" >/dev/null
manifest="$fake_r2/objects/vastora/releases/v$version/manifest.json"
test -f "$manifest"
test ! -e "$fake_r2/objects/vastora/releases/v$version/activated.json"
jq -e --arg version "$version" '.schema == 1 and .version == $version and (.assets | length) == 3' "$manifest" >/dev/null

"$script_dir/publish-installer-r2.sh" activate --version "$version" --bucket "$bucket" --endpoint "$endpoint" >/dev/null
cmp -s "$manifest" "$fake_r2/objects/vastora/releases/v$version/activated.json"
cmp -s "$manifest" "$fake_r2/objects/vastora/current.json"
"$script_dir/publish-installer-r2.sh" activate --version "$version" --bucket "$bucket" --endpoint "$endpoint" >/dev/null

current_digest="$(sha256sum "$fake_r2/objects/vastora/current.json" | awk 'NR == 1 {print $1}')"
downgrade_version="0.1.0-alpha.9"
create_bundle "$downgrade_version" downgrade
"$script_dir/create-installer-release-manifest.sh" --version "$downgrade_version" --source-dir "$source_dir" >/dev/null
"$script_dir/publish-installer-r2.sh" stage --version "$downgrade_version" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" >/dev/null
if "$script_dir/publish-installer-r2.sh" activate --version "$downgrade_version" --bucket "$bucket" --endpoint "$endpoint" >/dev/null 2>&1; then
  echo "R2 publication moved the current release pointer backward." >&2
  exit 1
fi
test "$(sha256sum "$fake_r2/objects/vastora/current.json" | awk 'NR == 1 {print $1}')" = "$current_digest"
test ! -e "$fake_r2/objects/vastora/releases/v$downgrade_version/activated.json"

newer_version="0.1.0-alpha.11"
create_bundle "$newer_version" newer
"$script_dir/create-installer-release-manifest.sh" --version "$newer_version" --source-dir "$source_dir" >/dev/null
"$script_dir/publish-installer-r2.sh" stage --version "$newer_version" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" >/dev/null
"$script_dir/publish-installer-r2.sh" activate --version "$newer_version" --bucket "$bucket" --endpoint "$endpoint" >/dev/null
jq -e --arg version "$newer_version" '.version == $version' "$fake_r2/objects/vastora/current.json" >/dev/null

create_bundle "$newer_version" changed
"$script_dir/create-installer-release-manifest.sh" --version "$newer_version" --source-dir "$source_dir" >/dev/null
if "$script_dir/publish-installer-r2.sh" stage --version "$newer_version" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" >/dev/null 2>&1; then
  echo "Immutable R2 release assets accepted changed content." >&2
  exit 1
fi

mkdir -p "$fake_r2/objects/vastora/releases/v0.0.0-stale"
stale_number=1
while [ "$stale_number" -le 1002 ]; do
  : > "$fake_r2/objects/vastora/releases/v0.0.0-stale/object-$stale_number"
  stale_number=$((stale_number + 1))
done
"$script_dir/reconcile-installer-r2.sh" --bucket "$bucket" --endpoint "$endpoint" --dry-run > "$temporary_dir/dry-run.txt"
grep -Fq "vastora/releases/v$version/install.sh" "$temporary_dir/dry-run.txt"

printf 'unexpected\n' > "$fake_r2/objects/vastora/unexpected-object"
if "$script_dir/reconcile-installer-r2.sh" --bucket "$bucket" --endpoint "$endpoint" >/dev/null 2>&1; then
  echo "R2 reconciliation accepted an unknown Vastora prefix." >&2
  exit 1
fi
rm "$fake_r2/objects/vastora/unexpected-object"

touch "$fake_r2/change-pointer"
if "$script_dir/reconcile-installer-r2.sh" --bucket "$bucket" --endpoint "$endpoint" >/dev/null 2>&1; then
  echo "R2 reconciliation deleted after the current pointer changed." >&2
  exit 1
fi
rm "$fake_r2/change-pointer" "$fake_r2/current-read-count"

touch "$fake_r2/fail-delete"
if "$script_dir/reconcile-installer-r2.sh" --bucket "$bucket" --endpoint "$endpoint" >/dev/null 2>&1; then
  echo "R2 reconciliation ignored a per-object deletion error." >&2
  exit 1
fi
rm "$fake_r2/fail-delete"

touch "$fake_r2/skip-delete"
if "$script_dir/reconcile-installer-r2.sh" --bucket "$bucket" --endpoint "$endpoint" >/dev/null 2>&1; then
  echo "R2 reconciliation ignored post-delete leftovers." >&2
  exit 1
fi
rm "$fake_r2/skip-delete"

"$script_dir/reconcile-installer-r2.sh" --bucket "$bucket" --endpoint "$endpoint" >/dev/null
test "$(find "$fake_r2/objects/vastora" -type f | wc -l | tr -d ' ')" = "6"
test -f "$fake_r2/objects/vastora/current.json"
test -f "$fake_r2/objects/vastora/releases/v$newer_version/activated.json"
test ! -e "$fake_r2/objects/vastora/releases/v$version/manifest.json"

if "$script_dir/publish-installer-r2.sh" stage --version "0.1.0-other" --bucket "$bucket" --endpoint "$endpoint" --source-dir "$source_dir" >/dev/null 2>&1; then
  echo "R2 publication accepted a bundle for a different version." >&2
  exit 1
fi

echo "R2 installer publication tests: OK"
