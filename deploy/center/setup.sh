#!/bin/sh
set -eu

image=""
center_url=""
headscale_url=""
tls_cert=""
tls_key=""
center_port="443"
headscale_port="8443"

usage() {
  cat <<'EOF'
Usage: ./setup.sh --image IMAGE@sha256:DIGEST --center-url HTTPS_URL \
  --headscale-url HTTPS_URL --tls-cert FILE --tls-key FILE \
  [--center-port PORT] [--headscale-port PORT]
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --image) image="$2"; shift 2 ;;
    --center-url) center_url="$2"; shift 2 ;;
    --headscale-url) headscale_url="$2"; shift 2 ;;
    --tls-cert) tls_cert="$2"; shift 2 ;;
    --tls-key) tls_key="$2"; shift 2 ;;
    --center-port) center_port="$2"; shift 2 ;;
    --headscale-port) headscale_port="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$image" ] || [ -z "$center_url" ] || [ -z "$headscale_url" ] || [ -z "$tls_cert" ] || [ -z "$tls_key" ]; then
  echo "Image, both HTTPS URLs, and both TLS files are required." >&2
  usage >&2
  exit 2
fi
case "$image" in
  *@sha256:*) ;;
  *) echo "The Center image must be pinned by sha256 digest." >&2; exit 2 ;;
esac
case "$center_url" in
  https://*) ;;
  *) echo "Center URL must use HTTPS." >&2; exit 2 ;;
esac
case "$headscale_url" in
  https://*) ;;
  *) echo "Headscale URL must use HTTPS." >&2; exit 2 ;;
esac
case "$center_url$headscale_url" in
  *https://\[*) echo "Use DNS hostnames in Center and Headscale URLs; IPv6 literals are not supported." >&2; exit 2 ;;
esac
case "$center_url$headscale_url" in
  *\?*|*\#*|*@*) echo "URLs cannot contain credentials, query parameters, or fragments." >&2; exit 2 ;;
esac
case "$center_port:$headscale_port" in
  *[!0-9:]*|:*|*:) echo "Ports must be numeric." >&2; exit 2 ;;
esac
if [ "$center_port" -lt 1 ] || [ "$center_port" -gt 65535 ] || [ "$headscale_port" -lt 1 ] || [ "$headscale_port" -gt 65535 ]; then
  echo "Ports must be between 1 and 65535." >&2
  exit 2
fi

url_authority() {
  value="${1#https://}"
  printf '%s\n' "${value%%/*}"
}
url_host() {
  authority="$(url_authority "$1")"
  printf '%s\n' "${authority%%:*}"
}
url_port() {
  authority="$(url_authority "$1")"
  case "$authority" in
    *:*) printf '%s\n' "${authority##*:}" ;;
    *) printf '443\n' ;;
  esac
}
for configured_url in "$center_url" "$headscale_url"; do
  authority="$(url_authority "$configured_url")"
  path="${configured_url#https://$authority}"
  if [ -n "$path" ] && [ "$path" != "/" ]; then
    echo "Center and Headscale URLs cannot contain a path." >&2
    exit 2
  fi
done
if [ "$(url_port "$center_url")" != "$center_port" ] || [ "$(url_port "$headscale_url")" != "$headscale_port" ]; then
  echo "URL ports must match --center-port and --headscale-port." >&2
  exit 2
fi

for required in docker install mktemp openssl sed; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required. Install the Docker Compose plugin first." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but the daemon is not running." >&2
  exit 1
fi
if [ ! -r "$tls_cert" ] || [ ! -r "$tls_key" ]; then
  echo "TLS certificate or private key is not readable." >&2
  exit 1
fi
if ! openssl x509 -in "$tls_cert" -noout -checkend 86400 >/dev/null 2>&1; then
  echo "TLS certificate is invalid or expires within 24 hours." >&2
  exit 1
fi
cert_fingerprint="$(openssl x509 -in "$tls_cert" -pubkey -noout | openssl pkey -pubin -outform DER 2>/dev/null | openssl dgst -sha256)"
key_fingerprint="$(openssl pkey -in "$tls_key" -pubout -outform DER 2>/dev/null | openssl dgst -sha256)"
if [ -z "$cert_fingerprint" ] || [ "$cert_fingerprint" != "$key_fingerprint" ]; then
  echo "TLS certificate and private key do not match." >&2
  exit 1
fi
for certificate_host in "$(url_host "$center_url")" "$(url_host "$headscale_url")"; do
  if ! openssl x509 -in "$tls_cert" -noout -checkhost "$certificate_host" >/dev/null 2>&1; then
    echo "TLS certificate does not cover hostname: $certificate_host" >&2
    exit 1
  fi
done

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$script_dir"
if [ -e .env ] || [ -e generated/headscale.yaml ] || [ -e tls/tls.crt ] || [ -e tls/tls.key ]; then
  echo "A Center deployment already exists here. Existing configuration was not changed." >&2
  exit 1
fi
mkdir -p generated tls
deployment_configured=0
cleanup() {
  if [ "$deployment_configured" -ne 1 ]; then
    rm -f .env generated/headscale.yaml tls/tls.crt tls/tls.key
  fi
}
trap cleanup EXIT HUP INT TERM
install -m 0644 "$tls_cert" tls/tls.crt
install -m 0600 "$tls_key" tls/tls.key
sed "s|^server_url:.*|server_url: $headscale_url|" headscale/config.yaml > generated/headscale.yaml
chmod 0600 generated/headscale.yaml

temporary_env="$(mktemp "${TMPDIR:-/tmp}/vastora-center-env.XXXXXX")"
trap 'rm -f "$temporary_env"; cleanup' EXIT HUP INT TERM
{
  printf 'VASTORA_CENTER_IMAGE=%s\n' "$image"
  printf 'VASTORA_CENTER_PORT=%s\n' "$center_port"
  printf 'VASTORA_HEADSCALE_PORT=%s\n' "$headscale_port"
  printf 'VASTORA_HEADSCALE_CONFIG=./generated/headscale.yaml\n'
} > "$temporary_env"
chmod 0600 "$temporary_env"
mv "$temporary_env" .env

echo "Validating the deployment..."
docker compose config --quiet
deployment_configured=1
echo "Starting Center and Headscale..."
docker compose up -d

attempt=0
until docker compose exec -T headscale headscale health >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "Headscale did not become healthy. Run: docker compose logs headscale" >&2
    exit 1
  fi
  sleep 2
done

api_key_file="generated/headscale-api-key.txt"
docker compose exec -T headscale headscale apikeys create --expiration 365d > "$api_key_file"
chmod 0600 "$api_key_file"
trap - EXIT HUP INT TERM

echo "Center is starting at: $center_url"
echo "Headscale API key saved to: $script_dir/$api_key_file"
echo "Open Center, create the administrator, then connect Headscale from Network settings."
