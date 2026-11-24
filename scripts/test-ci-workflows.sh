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
require_line "$ci_workflow" '    name: Go tests (${{ matrix.shard }}/6)'
require_line "$ci_workflow" '          SHARDS: 6'
require_line "$ci_workflow" '          WORKERS: 4'
require_line "$ci_workflow" '        run: sh scripts/run-ci-go-tests.sh'
require_line "$ci_workflow" '      - run: make go-format-check go-static-check'
require_line "$ci_workflow" '    name: Host and network integration'
require_line "$ci_workflow" '    name: Go cross-compile'
require_line "$ci_workflow" '    name: Privacy fixtures'
require_line "$ci_workflow" '        run: make privacy-check'
require_line "$ci_workflow" '    if: always() && !cancelled()'
require_line "$ci_workflow" '    needs: [changes, composite-runtime, privacy, go-tests, go-packages, go-quality, integration, go-build, web, deployment, security, release-metadata]'
require_line "$ci_workflow" '          GO_PACKAGES_RESULT: ${{ needs.go-packages.result }}'
require_line "$ci_workflow" '          sh scripts/run-composite-runtime-lab.sh "$RUNNER_TEMP/glivela-xray"'
require_line "$ci_workflow" '          docker pull "$GLIVELA_XRAY_IMAGE"'
require_line "$ci_workflow" '    name: Release metadata'
require_line "$ci_workflow" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'
require_line "$ci_workflow" '        run: scripts/classify-ci-changes.sh --git ci "$BASE_SHA" "$HEAD_SHA" >> "$GITHUB_OUTPUT"'
require_line "$codeql_workflow" '        run: scripts/classify-ci-changes.sh --git codeql "$BASE_SHA" "$HEAD_SHA" >> "$GITHUB_OUTPUT"'
require_line "$codeql_workflow" "    if: github.event_name != 'pull_request' || needs.changes.outputs.go == 'true' || needs.changes.outputs.full == 'true'"
require_line "$codeql_workflow" "    if: github.event_name != 'pull_request' || needs.changes.outputs.javascript == 'true' || needs.changes.outputs.full == 'true'"
require_line "$codeql_workflow" '    if: always() && !cancelled()'
require_line "$cache_workflow" '    name: Warm dependency caches'
require_line "$ci_workflow" '        run: scripts/check-runtime-image-platforms.sh'
require_line "$ci_workflow" '          go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --log-opts="$log_opts" .'
require_line "$codeql_workflow" '    name: CodeQL / gate'
if grep -Fq '  schedule:' "$ci_workflow"; then
  echo 'ci.yml must not run redundant weekly scheduled full CI.' >&2
  exit 1
fi
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
mkdir -p "$policy_dir/bin" "$policy_dir/internal/center"
cat > "$policy_dir/bin/go" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CI_GO_LOG"
case "$*" in
  *' -c '*)
    while [ "$1" != -o ]; do shift; done
    shift
    cat > "$1" <<'BINARY'
#!/bin/sh
printf '%s\n' "$*" >> "$CI_GO_LOG"
case "$*" in *-test.list*) printf 'TestOne\nTestTwo\nExampleOne\nFuzzOne\n' ;; esac
BINARY
    chmod +x "$1" ;;
  list*) printf 'github.com/petauron/vastora/internal/center\ngithub.com/petauron/vastora/internal/agent\n' ;;
esac
EOF
chmod +x "$policy_dir/bin/go"
for version in 0.1.0-alpha.317 0.1.0-alpha 0.1.0-beta.1 0.1.0; do
  printf '%s\n' "$version" > "$policy_dir/version.txt"
  : > "$policy_dir/go.log"
  (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" CI_GO_LOG="$policy_dir/go.log" RUNNER_TEMP="$policy_dir" SHARD=1 SHARDS=4 sh "$project_dir/scripts/run-ci-go-tests.sh")
  if grep -Fq 'internal/agent' "$policy_dir/go.log"; then echo 'Other packages must not run inside Center shards' >&2; exit 1; fi
  (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" CI_GO_LOG="$policy_dir/go.log" RUNNER_TEMP="$policy_dir" SUITE=packages sh "$project_dir/scripts/run-ci-go-tests.sh")
  case "$version" in
    *-alpha*)
      if grep -Fq -- '-race' "$policy_dir/go.log"; then echo 'Alpha executed race' >&2; exit 1; fi ;;
    *)
      grep -Fq -- 'test -race' "$policy_dir/go.log" ;;
  esac
  grep -Fq -- '-test.run' "$policy_dir/go.log"
  grep -Fq 'internal/agent' "$policy_dir/go.log"
done

# Placement may change, coverage may not. Include new tests, examples and fuzz
# seeds absent from the timing file, plus a removed timing entry.
printf '%s\n' 'TestSlow 12' 'TestMedium 6' 'TestRemoved 99' > "$policy_dir/weights"
printf '%s\n' TestSlow TestMedium TestNew TestNewTwo ExampleNew FuzzNew > "$policy_dir/list"
: > "$policy_dir/selected"
for shard in 1 2 3 4; do
  SHARD="$shard" SHARDS=4 sh "$project_dir/scripts/select-center-tests.sh" "$policy_dir/weights" < "$policy_dir/list" > "$policy_dir/shard-$shard"
  test -s "$policy_dir/shard-$shard"
  cat "$policy_dir/shard-$shard" >> "$policy_dir/selected"
done
LC_ALL=C sort "$policy_dir/list" > "$policy_dir/expected"
LC_ALL=C sort "$policy_dir/selected" > "$policy_dir/actual"
cmp "$policy_dir/expected" "$policy_dir/actual"
grep -Fxq TestSlow "$policy_dir/shard-1"
grep -Fxq TestMedium "$policy_dir/shard-2"
if SHARD=0 SHARDS=4 sh "$project_dir/scripts/select-center-tests.sh" "$policy_dir/weights" < "$policy_dir/list"; then
  echo 'Invalid shard was accepted' >&2; exit 1
fi

# Both suites must propagate Go test failure without a logging pipeline hiding it.
printf '%s\n' '#!/bin/sh' 'exit 17' > "$policy_dir/bin/go"
for suite in center packages; do
  if (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" RUNNER_TEMP="$policy_dir" SUITE="$suite" SHARD=1 SHARDS=4 sh "$project_dir/scripts/run-ci-go-tests.sh"); then
    echo 'Go failure was swallowed' >&2; exit 1
  fi
done

# Multi-worker sub-sharding must cover every test once and propagate any worker failure.
cat > "$policy_dir/bin/go" <<'EOF'
#!/bin/sh
case "$*" in
  *' -c '*)
    while [ "$1" != -o ]; do shift; done
    shift
    cat > "$1" <<'BINARY'
#!/bin/sh
case "$*" in
  *-test.list*)
    printf 'Test01\nTest02\nTest03\nTest04\nTest05\nTest06\nTest07\nTest08\nTest09\nTest10\nTest11\nTest12\n'
    ;;
  *-test.run*)
    printf '%s\n' "$*" >> "$CI_GO_LOG"
    if [ -n "${FAIL_ON_TEST:-}" ]; then
      case "$*" in
        *"$FAIL_ON_TEST"*) exit 23 ;;
      esac
    fi
    ;;
esac
BINARY
    chmod +x "$1" ;;
esac
EOF
chmod +x "$policy_dir/bin/go"
: > "$policy_dir/workers.log"
for shard in 1 2 3; do
  (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" CI_GO_LOG="$policy_dir/workers.log" RUNNER_TEMP="$policy_dir" SHARD="$shard" SHARDS=3 WORKERS=4 sh "$project_dir/scripts/run-ci-go-tests.sh" >/dev/null)
done
printf 'Test01\nTest02\nTest03\nTest04\nTest05\nTest06\nTest07\nTest08\nTest09\nTest10\nTest11\nTest12\n' | LC_ALL=C sort > "$policy_dir/workers-expected"
sed -n 's/.*-test\.run \^(\(.*\))\$/\1/p' "$policy_dir/workers.log" | tr '|' '\n' | LC_ALL=C sort > "$policy_dir/workers-actual"
cmp "$policy_dir/workers-expected" "$policy_dir/workers-actual"

worker_failures=0
for shard in 1 2 3; do
  if ! (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" CI_GO_LOG="$policy_dir/workers-fail.log" FAIL_ON_TEST=Test07 RUNNER_TEMP="$policy_dir" SHARD="$shard" SHARDS=3 WORKERS=4 sh "$project_dir/scripts/run-ci-go-tests.sh" >/dev/null 2>&1); then
    worker_failures=$((worker_failures + 1))
  fi
done
if [ "$worker_failures" -ne 1 ]; then
  echo "Expected exactly one failing shard for failing worker Test07, got $worker_failures" >&2
  exit 1
fi
for bad_workers in 0 abc; do
  if (cd "$policy_dir" && PATH="$policy_dir/bin:$PATH" RUNNER_TEMP="$policy_dir" SHARD=1 SHARDS=3 WORKERS="$bad_workers" sh "$project_dir/scripts/run-ci-go-tests.sh" >/dev/null 2>&1); then
    echo "Invalid WORKERS=$bad_workers was accepted" >&2
    exit 1
  fi
done
ci_backend="$(printf '%s\n' 'internal/center/server.go' | "$classifier" --files ci)"
printf '%s\n' "$ci_backend" | grep -Fxq 'go=true'
printf '%s\n' "$ci_backend" | grep -Fxq 'integration=true'
for path in internal/center/agent_removal.go internal/center/business_handover.go internal/center/migrations/00124_completed_node_removals.sql cmd/vastora/node_lifecycle_integration_test.go; do
  classified="$(printf '%s\n' "$path" | "$classifier" --files ci)"
  printf '%s\n' "$classified" | grep -Fxq 'integration=true'
done
require_line "$ci_workflow" '          VASTORA_NODE_LIFECYCLE_INTEGRATION: "1"'
require_line "$ci_workflow" "        run: go test ./cmd/vastora -run '^TestCenterTwoAgentsLifecycle$' -count=1 -timeout=4m -v"
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
for path in scripts/select-center-tests.sh scripts/center-test-timings.txt; do
  classified="$(printf '%s\n' "$path" | "$classifier" --files ci)"
  printf '%s\n' "$classified" | grep -Fxq 'go=true'
  printf '%s\n' "$classified" | grep -Fxq 'deployment=true'
done
for path in scripts/classify-ci-changes.sh scripts/validate-release-metadata.sh scripts/compare-semver.awk .github/actions/setup-go-cached/action.yml; do
  classified="$(printf '%s\n' "$path" | "$classifier" --files ci)"
  printf '%s\n' "$classified" | grep -Fxq 'full=true'
done
# --files mode has no Git content to verify, so release metadata fails closed.
for profile in ci codeql; do
  classified="$(printf '%s\n' CHANGELOG.md version.txt .release-please-manifest.json | "$classifier" --files "$profile")"
  printf '%s\n' "$classified" | grep -Fxq 'full=true'
done

# --git mode unlocks the scoped version-only lane only when metadata is valid
# and the alpha/non-alpha race policy is unchanged; everything else fails closed.
git_repo="$policy_dir/git-repo"
mkdir -p "$git_repo/internal/center" "$git_repo/docs"
git -C "$git_repo" init -q
git -C "$git_repo" config user.name test
git -C "$git_repo" config user.email test@example.invalid
printf '%s\n' '0.2.0' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.2.0"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.2.0](https://example.invalid)' > "$git_repo/CHANGELOG.md"
printf '%s\n' 'package center' > "$git_repo/internal/center/sample.go"
printf '%s\n' '# Guide' > "$git_repo/docs/guide.md"
git -C "$git_repo" add -- .
git -C "$git_repo" commit -qm base-0-2-0
m0="$(git -C "$git_repo" rev-parse HEAD)"

git -C "$git_repo" checkout -qb pr-release "$m0"
printf '%s\n' '0.2.1' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.2.1"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.2.1](https://example.invalid)' '## [0.2.0](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm release-0-2-1
r1="$(git -C "$git_repo" rev-parse HEAD)"

ci_git_release="$(cd "$git_repo" && "$classifier" --git ci "$m0" "$r1")"
printf '%s\n' "$ci_git_release" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_release" | grep -Fxq 'security=true'
printf '%s\n' "$ci_git_release" | grep -Fxq 'full=false'
printf '%s\n' "$ci_git_release" | grep -Fxq 'go=false'
printf '%s\n' "$ci_git_release" | grep -Fxq 'web=false'
printf '%s\n' "$ci_git_release" | grep -Fxq 'deployment=false'
printf '%s\n' "$ci_git_release" | grep -Fxq 'integration=false'
printf '%s\n' "$ci_git_release" | grep -Fxq 'dependencies=false'
codeql_git_release="$(cd "$git_repo" && "$classifier" --git codeql "$m0" "$r1")"
printf '%s\n' "$codeql_git_release" | grep -Fxq 'go=false'
printf '%s\n' "$codeql_git_release" | grep -Fxq 'javascript=false'
printf '%s\n' "$codeql_git_release" | grep -Fxq 'full=false'

git -C "$git_repo" checkout -qb pr-release-extra "$r1"
printf '%s\n' 'extra note' > "$git_repo/README.md"
git -C "$git_repo" add -- README.md
git -C "$git_repo" commit -qm release-plus-readme
r_extra="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_extra="$(cd "$git_repo" && "$classifier" --git ci "$m0" "$r_extra")"
printf '%s\n' "$ci_git_extra" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_extra" | grep -Fxq 'full=true'

git -C "$git_repo" checkout -qb pr-release-bad "$m0"
printf '%s\n' '0.2.1' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.2.9"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.2.1](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm release-mismatch
r_bad="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_bad="$(cd "$git_repo" && "$classifier" --git ci "$m0" "$r_bad")"
printf '%s\n' "$ci_git_bad" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_bad" | grep -Fxq 'full=true'

git -C "$git_repo" checkout -qb main-ahead "$m0"
printf '%s\n' 'package center // updated' > "$git_repo/internal/center/sample.go"
git -C "$git_repo" add -- internal/center/sample.go
git -C "$git_repo" commit -qm main-go-change
m1="$(git -C "$git_repo" rev-parse HEAD)"

git -C "$git_repo" checkout -qb pr-docs "$m0"
printf '%s\n' '# Guide updated' > "$git_repo/docs/guide.md"
git -C "$git_repo" add -- docs/guide.md
git -C "$git_repo" commit -qm docs-only
d1="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_docs_behind="$(cd "$git_repo" && "$classifier" --git ci "$m1" "$d1")"
printf '%s\n' "$ci_git_docs_behind" | grep -Fxq 'go=false'
printf '%s\n' "$ci_git_docs_behind" | grep -Fxq 'full=false'
ci_git_release_behind="$(cd "$git_repo" && "$classifier" --git ci "$m1" "$r1")"
printf '%s\n' "$ci_git_release_behind" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_release_behind" | grep -Fxq 'full=false'

git -C "$git_repo" checkout -qb main-released "$m0"
printf '%s\n' '0.2.1' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.2.1"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.2.1](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm main-already-0-2-1
m2="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_stale_release="$(cd "$git_repo" && "$classifier" --git ci "$m2" "$r1")"
printf '%s\n' "$ci_git_stale_release" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_stale_release" | grep -Fxq 'full=true'

git -C "$git_repo" checkout -qb alpha-base "$m0"
printf '%s\n' '0.1.0-alpha.15' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.1.0-alpha.15"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.1.0-alpha.15](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm alpha-15
a0="$(git -C "$git_repo" rev-parse HEAD)"
printf '%s\n' '0.1.0-alpha.16' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.1.0-alpha.16"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.1.0-alpha.16](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm alpha-16
a1="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_alpha="$(cd "$git_repo" && "$classifier" --git ci "$a0" "$a1")"
printf '%s\n' "$ci_git_alpha" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_alpha" | grep -Fxq 'full=false'

printf '%s\n' '0.2.0' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.2.0"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.2.0](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm graduate-to-0-2-0
a_grad="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_grad="$(cd "$git_repo" && "$classifier" --git ci "$a1" "$a_grad")"
printf '%s\n' "$ci_git_grad" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_grad" | grep -Fxq 'full=true'
codeql_git_grad="$(cd "$git_repo" && "$classifier" --git codeql "$a1" "$a_grad")"
printf '%s\n' "$codeql_git_grad" | grep -Fxq 'full=true'

printf '%s\n' '0.2.1-alpha.1' > "$git_repo/version.txt"
printf '%s\n' '{".":"0.2.1-alpha.1"}' > "$git_repo/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.2.1-alpha.1](https://example.invalid)' > "$git_repo/CHANGELOG.md"
git -C "$git_repo" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$git_repo" commit -qm return-to-alpha
a_rev="$(git -C "$git_repo" rev-parse HEAD)"
ci_git_rev="$(cd "$git_repo" && "$classifier" --git ci "$a_grad" "$a_rev")"
printf '%s\n' "$ci_git_rev" | grep -Fxq 'release_metadata=true'
printf '%s\n' "$ci_git_rev" | grep -Fxq 'full=true'
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
require_line "$ci_workflow" 'sudo -- "$RUNNER_TEMP/package-systemd.test" -test.run '\''^TestGlivelaHostKernelPreflight$'\'' -test.v'
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
# Main only compiles cache inputs. PR and manual CI retain real tests.
build_cache_workflow="$project_dir/.github/workflows/build-cache.yml"
require_line "$build_cache_workflow" '    branches: [main]'
require_line "$build_cache_workflow" "      - '.github/workflows/build-cache.yml'"
require_line "$build_cache_workflow" '    name: Warm compiled packages (no tests)'
require_line "$build_cache_workflow" 'go test -c -tags=integration'
require_line "$codeql_workflow" '  push:'
if grep -Fq '  push:' "$ci_workflow"; then
  echo 'Main pushes must not repeat integration tests for cache warming.' >&2
  exit 1
fi
if grep -E 'sudo | -test\.|go test ' "$build_cache_workflow" | grep -v 'go test -c '; then
  echo 'Build cache workflow must compile without executing tests.' >&2
  exit 1
fi
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
