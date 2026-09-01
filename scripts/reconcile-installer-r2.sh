#!/bin/sh
set -eu

bucket=""
endpoint=""
dry_run=false
version_out=""

usage() {
  cat <<'EOF'
Usage: reconcile-installer-r2.sh --bucket BUCKET --endpoint HTTPS_URL [--dry-run] [--version-out FILE]

Validates vastora/current.json, protects every object referenced by it, and
deletes only stale objects below the exact vastora/releases/ prefix.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --bucket) bucket="${2:-}"; shift 2 ;;
    --endpoint) endpoint="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    --version-out) version_out="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$bucket" in *[!a-z0-9.-]*|'') echo "R2 bucket name is invalid." >&2; exit 2 ;; esac
case "$endpoint" in https://*.r2.cloudflarestorage.com) ;; *) echo "R2 endpoint must use the Cloudflare HTTPS endpoint." >&2; exit 2 ;; esac
for required in awk aws basename cmp grep jq mktemp sha256sum sort split tr wc; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done

current_key="vastora/current.json"
release_root="vastora/releases/"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-r2-reconcile.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

aws_r2() {
  aws --cli-connect-timeout 10 --cli-read-timeout 60 s3api "$@" --endpoint-url "$endpoint" --no-cli-pager
}

validate_manifest() {
  manifest_path="$1"
  manifest_version="$(jq -er '.version | strings' "$manifest_path")"
  if ! printf '%s' "$manifest_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
    echo "R2 current release pointer contains an invalid version." >&2
    return 1
  fi
  manifest_prefix="vastora/releases/v$manifest_version/"
  if ! jq -e --arg version "$manifest_version" --arg prefix "$manifest_prefix" '
    (.schema == 1) and (.version == $version) and
    ((.assets | keys | sort) == ["install.sh", "vastora-center-install.tar.gz", "vastora-center-install.tar.gz.sha256"]) and
    (.assets["install.sh"].key == ($prefix + "install.sh")) and
    (.assets["vastora-center-install.tar.gz"].key == ($prefix + "vastora-center-install.tar.gz")) and
    (.assets["vastora-center-install.tar.gz.sha256"].key == ($prefix + "vastora-center-install.tar.gz.sha256")) and
    (all(.assets[]; .sha256 | test("^[0-9a-f]{64}$")))
  ' "$manifest_path" >/dev/null; then
    echo "R2 current release pointer is invalid." >&2
    return 1
  fi
  printf '%s\n' "$manifest_version"
}

verify_remote_digest() {
  key="$1"
  expected_digest="$2"
  actual_digest="$(aws_r2 head-object --bucket "$bucket" --key "$key" --query 'Metadata.sha256' --output text 2>/dev/null || true)"
  if [ "$actual_digest" != "$expected_digest" ]; then
    echo "Protected R2 object is missing or has mismatched metadata: $key" >&2
    return 1
  fi
}

list_keys() {
  output="$1"
  : > "$output"
  continuation=""
  page_number=0
  while :; do
    page_number=$((page_number + 1))
    page="$temporary_dir/list-$page_number.json"
    if [ -n "$continuation" ]; then
      aws_r2 list-objects-v2 --bucket "$bucket" --prefix "vastora/" --max-keys 1000 --continuation-token "$continuation" > "$page"
    else
      aws_r2 list-objects-v2 --bucket "$bucket" --prefix "vastora/" --max-keys 1000 > "$page"
    fi
    if ! jq -e 'all(.Contents[]?;
      (.Key | type == "string") and
      (.Key | test("^vastora/[A-Za-z0-9._/-]+$")) and
      (.Key | contains("//") | not) and
      (.Key | split("/") | all(. != "" and . != "." and . != ".."))
    )' "$page" >/dev/null; then
      echo "R2 listing contains an unsafe or out-of-scope object key." >&2
      return 1
    fi
    jq -r '.Contents[]?.Key' "$page" >> "$output"
    if [ "$(jq -r '.IsTruncated // false' "$page")" != "true" ]; then
      break
    fi
    continuation="$(jq -er '.NextContinuationToken | strings | select(length > 0)' "$page")"
  done
  sort -u "$output" -o "$output"
}

current_manifest="$temporary_dir/current.json"
aws_r2 get-object --bucket "$bucket" --key "$current_key" "$current_manifest" >/dev/null
version="$(validate_manifest "$current_manifest")"
prefix="vastora/releases/v$version"
manifest_key="$prefix/manifest.json"
activated_key="$prefix/activated.json"

for asset in install.sh vastora-center-install.tar.gz vastora-center-install.tar.gz.sha256; do
  asset_key="$(jq -r --arg asset "$asset" '.assets[$asset].key' "$current_manifest")"
  asset_digest="$(jq -r --arg asset "$asset" '.assets[$asset].sha256' "$current_manifest")"
  verify_remote_digest "$asset_key" "$asset_digest"
done
manifest_digest="$(sha256sum "$current_manifest" | awk 'NR == 1 {print tolower($1)}')"
verify_remote_digest "$current_key" "$manifest_digest"
verify_remote_digest "$manifest_key" "$manifest_digest"
verify_remote_digest "$activated_key" "$manifest_digest"
for key in "$manifest_key" "$activated_key"; do
  copy="$temporary_dir/$(basename "$key")"
  aws_r2 get-object --bucket "$bucket" --key "$key" "$copy" >/dev/null
  if ! cmp -s "$current_manifest" "$copy"; then
    echo "Protected R2 release manifest differs from vastora/current.json: $key" >&2
    exit 1
  fi
done

expected="$temporary_dir/expected-keys"
{
  printf '%s\n' "$current_key" "$manifest_key" "$activated_key"
  jq -r '.assets[].key' "$current_manifest"
} | sort -u > "$expected"
if [ "$(wc -l < "$expected" | tr -d ' ')" != "6" ]; then
  echo "R2 current release does not resolve to exactly six protected objects." >&2
  exit 1
fi

listed="$temporary_dir/listed-keys"
list_keys "$listed"
while IFS= read -r protected; do
  if ! grep -Fxq "$protected" "$listed"; then
    echo "Protected R2 object is absent from the Vastora listing: $protected" >&2
    exit 1
  fi
done < "$expected"

stale="$temporary_dir/stale-keys"
: > "$stale"
while IFS= read -r key; do
  if grep -Fxq "$key" "$expected"; then
    continue
  fi
  case "$key" in
    vastora/releases/*) printf '%s\n' "$key" >> "$stale" ;;
    *) echo "Unexpected object outside the managed Vastora release prefix: $key" >&2; exit 1 ;;
  esac
done < "$listed"
sort -u "$stale" -o "$stale"

if [ -n "$version_out" ]; then
  printf '%s\n' "$version" > "$version_out"
fi
if [ "$dry_run" = true ]; then
  while IFS= read -r key; do
    [ -n "$key" ] && printf 'Would delete: %s\n' "$key"
  done < "$stale"
  echo "Dry run protected Vastora $version and found $(wc -l < "$stale" | tr -d ' ') stale object(s)."
  exit 0
fi

if [ -s "$stale" ]; then
  batches="$temporary_dir/delete-batch-"
  split -l 1000 -d -a 6 "$stale" "$batches"
  for batch in "$batches"*; do
    pointer_check="$batch-current.json"
    aws_r2 get-object --bucket "$bucket" --key "$current_key" "$pointer_check" >/dev/null
    if ! cmp -s "$current_manifest" "$pointer_check"; then
      echo "R2 current release pointer changed during cleanup; refusing deletion." >&2
      exit 1
    fi
    request="$batch-request.json"
    response="$batch-response.json"
    jq -R -s '{Objects: (split("\n") | map(select(length > 0) | {Key: .})), Quiet: false}' "$batch" > "$request"
    aws_r2 delete-objects --bucket "$bucket" --delete "file://$request" > "$response"
    if ! jq -e '((.Errors // []) | length) == 0' "$response" >/dev/null; then
      jq -r '.Errors[] | "R2 delete failed for \(.Key): \(.Code): \(.Message)"' "$response" >&2
      exit 1
    fi
  done
fi

post_delete="$temporary_dir/post-delete-keys"
list_keys "$post_delete"
if ! cmp -s "$expected" "$post_delete"; then
  echo "R2 reconciliation left unexpected or missing Vastora objects." >&2
  exit 1
fi
final_pointer="$temporary_dir/final-current.json"
aws_r2 get-object --bucket "$bucket" --key "$current_key" "$final_pointer" >/dev/null
if ! cmp -s "$current_manifest" "$final_pointer"; then
  echo "R2 current release pointer changed during reconciliation." >&2
  exit 1
fi
echo "Reconciled R2 to the six protected objects for Vastora $version."
