#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
ci_workflow="$project_dir/.github/workflows/ci.yml"
codeql_workflow="$project_dir/.github/workflows/codeql.yml"
cache_workflow="$project_dir/.github/workflows/dependency-cache.yml"
center_dockerfile="$project_dir/Dockerfile.center"
classifier="$project_dir/scripts/classify-ci-changes.sh"

require_line() {
  file="$1"
  expected="$2"
  if ! grep -Fq -- "$expected" "$file"; then
    echo "$file is missing: $expected" >&2
    exit 1
  fi
}

require_line "$ci_workflow" '    name: CI / gate'
require_line "$center_dockerfile" 'COPY catalog/trust/ /app/catalog-trust/'
require_line "$project_dir/.dockerignore" '**/*_test.go'
require_line "$project_dir/deploy/center/compose.yaml" '${VASTORA_OFFICIAL_CATALOG_ROOT:-/app/catalog-trust/2.root.json}'
require_line "$project_dir/.github/workflows/release.yml" 'sh scripts/check-independent-catalog.sh --catalog internal/center/testdata/reviewed-catalog-v4.json --root-directory catalog/trust'
require_line "$ci_workflow" '    name: Go tests (${{ matrix.shard }}/4)'
require_line "$ci_workflow" '        run: sh scripts/run-ci-go-tests.sh'
require_line "$ci_workflow" '      - run: make go-format-check go-static-check'
require_line "$ci_workflow" '    name: Host and network integration'
require_line "$ci_workflow" '    name: Go cross-compile'
require_line "$ci_workflow" '    name: Privacy fixtures'
require_line "$ci_workflow" '        run: make privacy-check'
require_line "$ci_workflow" '    needs: [changes, composite-runtime, privacy, go-tests, go-quality, integration, go-build, web, deployment, security, release-metadata]'
require_line "$ci_workflow" '          sh scripts/run-composite-runtime-lab.sh "$RUNNER_TEMP/glivela-xray"'
require_line "$ci_workflow" '          docker pull "$GLIVELA_XRAY_IMAGE"'
require_line "$ci_workflow" '    name: Release metadata'
require_line "$ci_workflow" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'
require_line "$ci_workflow" '        run: scripts/classify-ci-changes.sh --git ci "$BASE_SHA" "$HEAD_SHA" >> "$GITHUB_OUTPUT"'
require_line "$codeql_workflow" '        run: scripts/classify-ci-changes.sh --git codeql "$BASE_SHA" "$HEAD_SHA" >> "$GITHUB_OUTPUT"'
require_line "$cache_workflow" '    name: Warm dependency caches'
require_line "$ci_workflow" '        run: scripts/check-runtime-image-platforms.sh'
require_line "$ci_workflow" '          go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --log-opts="$log_opts" .'
require_line "$codeql_workflow" '    name: CodeQL / gate'
# No hidden alpha bypass, repeated ordinary tests, PR image scan or direct race.
if grep -Eq 'VASTORA_CI_MODE|alpha-minimal|container-image-security|go test -race|make go-check|make go-race-check' "$ci_workflow" "$codeql_workflow"; then
  echo 'Retired CI modes or direct race/image checks returned.' >&2
  exit 1
fi
for job in composite-runtime integration; do
  condition="$(sed -n "/^  $job:/,/^    needs:/p" "$ci_workflow" | grep '^    if:')"
  printf '%s\n' "$condition" | grep -Fq "needs.changes.outputs.integration == 'true'"
done
require_line "$ci_workflow" "if: github.event_name != 'pull_request' || needs.changes.outputs.dependencies == 'true' || needs.changes.outputs.full == 'true'"

# Version policy is exercised with a fake Go command, never a network/build.
policy_dir="$(mktemp -d)"
trap 'rm -rf "$policy_dir"' EXIT HUP INT TERM
mkdir "$policy_dir/bin"
cat > "$policy_dir/bin/go" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CI_GO_LOG"
case "$*" in
  *-list*) printf 'TestOne\nTestTwo\nExampleOne\nFuzzOne\n' ;;
  list*) printf 'github.com/petauron/vastora/internal/center\ngithub.com/petauron/vastora/internal/agent\n' ;;
esac
EOF
chmod +x "$policy_dir/bin/go"
for version in 0.1.0-alpha.317 0.1.0-alpha 0.1.0-beta.1 0.1.0; do
  printf '%s\n' "$version" > "$policy_dir/version.txt"
  : > "$policy_dir/go.log"
  (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" CI_GO_LOG="$policy_dir/go.log" RUNNER_TEMP="$policy_dir" SHARD=1 SHARDS=4 sh "$project_dir/scripts/run-ci-go-tests.sh")
  case "$version" in
    *-alpha*)
      if grep -Fq -- '-race' "$policy_dir/go.log"; then echo 'Alpha executed race' >&2; exit 1; fi ;;
    *)
      grep -Fq -- 'test -race' "$policy_dir/go.log" ;;
  esac
  grep -Fq 'internal/center -run' "$policy_dir/go.log"
  grep -Fq 'internal/agent' "$policy_dir/go.log"
done
ci_backend="$(printf '%s\n' 'internal/center/server.go' | "$classifier" --files ci)"
printf '%s\n' "$ci_backend" | grep -Fxq 'go=true'
printf '%s\n' "$ci_backend" | grep -Fxq 'integration=false'
ci_host="$(printf '%s\n' 'internal/agent/host.go' | "$classifier" --files ci)"
printf '%s\n' "$ci_host" | grep -Fxq 'integration=true'
ci_release="$(printf '%s\n' 'version.txt' | "$classifier" --files ci)"
printf '%s\n' "$ci_release" | grep -Fxq 'full=true'
codeql_release="$(printf '%s\n' 'version.txt' | "$classifier" --files codeql)"
printf '%s\n' "$codeql_release" | grep -Fxq 'full=true'

# Lightweight edits must not start host experiments or full release checks.
for path in CHANGELOG.md scripts/publish-installer-r2.sh scripts/verify-installer-release.sh deploy/installer-worker/worker.js deploy/center/README.md; do
  classified="$(printf '%s\n' "$path" | "$classifier" --files ci)"
  printf '%s\n' "$classified" | grep -Fxq 'full=false'
  printf '%s\n' "$classified" | grep -Fxq 'integration=false'
  printf '%s\n' "$classified" | grep -Fxq 'release_metadata=false'
done
for path in scripts/run-composite-runtime-lab.sh scripts/build-landing-dante.sh scripts/install-landing-build-deps.sh deploy/landing/Dockerfile deploy/center/update-center.sh deploy/center/compose.yaml; do
  classified="$(printf '%s\n' "$path" | "$classifier" --files ci)"
  printf '%s\n' "$classified" | grep -Fxq 'integration=true'
done
ci_runner="$(printf '%s\n' scripts/run-ci-go-tests.sh | "$classifier" --files ci)"
printf '%s\n' "$ci_runner" | grep -Fxq 'go=true'
printf '%s\n' "$ci_runner" | grep -Fxq 'deployment=true'
printf '%s\n' "$ci_runner" | grep -Fxq 'integration=false'
for path in scripts/classify-ci-changes.sh .github/actions/setup-go-cached/action.yml; do
  classified="$(printf '%s\n' "$path" | "$classifier" --files ci)"
  printf '%s\n' "$classified" | grep -Fxq 'full=true'
done
# A genuine version PR still runs real checks in both workflows.
for profile in ci codeql; do
  classified="$(printf '%s\n' CHANGELOG.md version.txt .release-please-manifest.json | "$classifier" --files "$profile")"
  printf '%s\n' "$classified" | grep -Fxq 'full=true'
done
require_line "$ci_workflow" '--platform linux/amd64 --output "type=local,dest=$RUNNER_TEMP/dante-bundle"'
require_line "$ci_workflow" 'cp danted-linux-amd64.gz Dante-LICENSE Dante-NOTICE "$GITHUB_WORKSPACE/internal/dantebundle/assets/"'
require_line "$ci_workflow" 'sha256sum --check danted-linux-amd64.gz.sha256'
require_line "$ci_workflow" "hashFiles('deploy/landing/Dockerfile', 'scripts/build-landing-dante.sh', 'scripts/install-landing-build-deps.sh')"
for step in 'Set up native landing artifact builder' 'Build native landing artifact on cache miss'; do
  step_block="$(sed -n "/^      - name: $step$/,/^      - name:/p" "$ci_workflow")"
  printf '%s\n' "$step_block" | grep -Fq "if: steps.dante.outputs.cache-hit != 'true'"
done
# Caching may skip compilation, never the kernel or lifecycle tests.
integration_block="$(sed -n '/^  integration:/,/^  go-build:/p' "$ci_workflow")"
test_block="$(printf '%s\n' "$integration_block" | sed -n '/^      - name: Verify landing kernel lease/,/^      - name:/p')"
if printf '%s\n' "$test_block" | grep -Fq 'cache-hit'; then
  echo 'Cache hits must still execute integration tests.' >&2
  exit 1
fi
require_line "$ci_workflow" 'sudo -- "$RUNNER_TEMP/package-systemd.test" -test.run '\''^TestMeridianHostKernelPreflight$'\'' -test.v'
require_line "$ci_workflow" 'sudo env VASTORA_NATIVE_LANDING_INTEGRATION=1 "$RUNNER_TEMP/package-systemd.test"'
if [ "$(grep -c 'go test -c -tags=integration .* ./internal/agent' "$ci_workflow")" -ne 1 ] ||
   grep -Fq 'dante/linux_amd64/' "$ci_workflow"; then
  echo 'Host integration must reuse one Agent test binary and single-platform output.' >&2
  exit 1
fi
# Retired specialist workflows must not return through a stale reference.
if [ -e "$project_dir/.github/workflows/catalog-check.yml" ] ||
   [ -d "$project_dir/scripts/meridian-fullcone-lab" ]; then
  echo 'Retired catalog/UDP CI resources must remain removed.' >&2
  exit 1
fi

if grep -Fq 'cache-to: type=gha' "$ci_workflow"; then
  echo 'Pull-request image builds must not write GitHub Actions caches.' >&2
  exit 1
fi
ci_frontend="$(printf '%s\n' 'web/src/App.tsx' | "$classifier" --files ci)"
ci_lockfile="$(printf '%s\n' 'web/package-lock.json' | "$classifier" --files ci)"
codeql_go="$(printf '%s\n' 'internal/center/server.go' | "$classifier" --files codeql)"
ci_openapi="$(printf '%s\n' 'docs/openapi.json' | "$classifier" --files ci)"
ci_catalog="$(printf '%s\n' 'internal/center/testdata/reviewed-catalog-v4.json' | "$classifier" --files ci)"
ci_catalog_root="$(printf '%s\n' 'catalog/trust/1.root.json' | "$classifier" --files ci)"
ci_catalog_rotated_root="$(printf '%s\n' 'catalog/trust/2.root.json' | "$classifier" --files ci)"
if ! printf '%s\n' "$ci_catalog" | grep -Fq 'dependencies=false' || ! printf '%s\n' "$ci_catalog_root" | grep -Fq 'integration=true' || ! printf '%s\n' "$ci_catalog_rotated_root" | grep -Fq 'integration=true'; then
  echo 'Catalog content is independently published; reviewed bootstrap roots must be packaged with Center.' >&2
  exit 1
fi
if ! printf '%s\n' "$ci_frontend" | grep -Fq 'web=true' || ! printf '%s\n' "$ci_frontend" | grep -Fq 'dependencies=false'; then
  echo 'Ordinary frontend source changes were classified incorrectly.' >&2
  exit 1
fi
if ! printf '%s\n' "$ci_lockfile" | grep -Fq 'web=true' || ! printf '%s\n' "$ci_lockfile" | grep -Fq 'dependencies=true'; then
  echo 'Container dependency changes were classified incorrectly.' >&2
  exit 1
fi
if ! printf '%s\n' "$codeql_go" | grep -Fq 'go=true' || ! printf '%s\n' "$codeql_go" | grep -Fq 'javascript=false'; then
  echo 'CodeQL language changes were classified incorrectly.' >&2
  exit 1
fi
if ! printf '%s\n' "$ci_openapi" | grep -Fq 'go=true'; then
  echo 'OpenAPI contract changes must run the Go contract validator.' >&2
  exit 1
fi
if grep -Fq 'dorny/paths-filter' "$ci_workflow" "$codeql_workflow" || grep -Fq 'gitleaks/gitleaks-action' "$ci_workflow"; then
  echo 'CI must not require additional third-party Actions permissions.' >&2
  exit 1
fi
if grep -Fq 'runtime-image-platforms:' "$ci_workflow"; then
  echo 'Runtime image validation should share the deployment runner.' >&2
  exit 1
fi
for architecture in amd64 arm64; do
  require_line "$center_dockerfile" "FROM scratch AS dante-$architecture"
  require_line "$center_dockerfile" "COPY --link --from=dante-build-$architecture /out/ /out/"
  require_line "$center_dockerfile" "COPY --link --from=dante-$architecture /out/ /src/internal/dantebundle/assets/"
  require_line "$center_dockerfile" "FROM go-source AS compile-$architecture"
  require_line "$center_dockerfile" "FROM compile-$architecture AS binary-$architecture"
  compile_stage="$(sed -n "/^FROM go-source AS compile-$architecture$/,/^FROM compile-$architecture AS binary-$architecture$/p" "$center_dockerfile")"
  if printf '%s\n' "$compile_stage" | grep -Eq 'VASTORA_VERSION|type=cache'; then
    echo 'Package compilation must be version-independent and exportable.' >&2
    exit 1
  fi
  build_count="$(grep -F "GOARCH=$architecture " "$center_dockerfile" | grep -c 'go build')"
  if [ "$build_count" -ne 1 ]; then
    echo "Center image must compile $architecture exactly once; found $build_count builds." >&2
    exit 1
  fi
done
require_line "$center_dockerfile" 'FROM scratch AS web-assets'
require_line "$center_dockerfile" 'COPY --link --from=web-build /src/web/dist/ /dist/'
require_line "$center_dockerfile" 'COPY --link --from=web-assets /dist/ /app/web/dist/'
require_line "$center_dockerfile" "find /go/pkg/mod/cache/download -type f -name '*.zip' -delete"
require_line "$center_dockerfile" 'COPY --link cmd/ ./cmd/'
require_line "$center_dockerfile" 'COPY --link internal/ ./internal/'
if grep -Fq 'target=/go/pkg/mod' "$center_dockerfile"; then
  echo 'Center image dependency downloads must live in an exportable layer, not an unexported cache mount.' >&2
  exit 1
fi
# Only the integration job warms compiled caches on main; dependency downloads
# retain their existing path-filtered workflow. Never run the whole PR matrix.
require_line "$ci_workflow" '    branches: [main]'
require_line "$ci_workflow" "      - 'deploy/landing/Dockerfile'"
require_line "$ci_workflow" "      - '.github/workflows/ci.yml'"
for job in go-tests go-quality go-build web deployment security composite-runtime; do
  condition="$(sed -n "/^  $job:/,/^    needs:/p" "$ci_workflow" | grep '^    if:')"
  if printf '%s\n' "$condition" | grep -Fq "github.event_name == 'push'"; then
    echo "$job must not duplicate PR checks on main pushes." >&2
    exit 1
  fi
done
for workflow in "$ci_workflow" "$codeql_workflow" "$cache_workflow"; do
  if ! grep -Fq 'timeout-minutes:' "$workflow"; then
    echo "$workflow has no job timeouts." >&2
    exit 1
  fi
done

echo "CI workflow policy test passed"

# Download-only warming must not populate a combined immutable build cache.
go_cache_action="$project_dir/.github/actions/setup-go-cached/action.yml"
require_line "$go_cache_action" 'path: ${{ steps.paths.outputs.modules }}'
require_line "$go_cache_action" 'path: ${{ steps.paths.outputs.build }}'
require_line "$go_cache_action" "\${{ github.job }}-\${{ hashFiles('go.sum') }}-\${{ github.sha }}"
require_line "$cache_workflow" "          build-cache: 'false'"
