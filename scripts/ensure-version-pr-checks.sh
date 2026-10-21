#!/bin/sh
set -eu

# Scheduling only, not a CI gate. Never approve, cancel or rerun workflows,
# write check results, or treat a different commit's success as this PR's CI.
branch="${1:-}"
head_sha="${2:-}"
repository="${GITHUB_REPOSITORY:-}"
if [ "$#" -ne 2 ] || [ "$repository" != petauron/vastora ] ||
   [ "$branch" != release-please--branches--main--components--vastora ] ||
   ! printf '%s' "$head_sha" | grep -Eq '^[a-f0-9]{40}$'; then
  echo 'Expected the managed Vastora version PR branch and its validated head SHA.' >&2
  exit 2
fi

require_head() {
  pull_pages="$(gh api --method GET "repos/$repository/pulls" \
    -f state=open -f base=main -f "head=petauron:$branch" --paginate --slurp)"
  pr="$(printf '%s' "$pull_pages" |
    jq -ce --arg repository "$repository" --arg branch "$branch" --arg sha "$head_sha" '
      [.[][] | select(.head.ref == $branch)] |
      if length == 1 and .[0].user.login == "github-actions[bot]" and
         .[0].head.repo.full_name == $repository and .[0].base.repo.full_name == $repository and
         .[0].base.ref == "main" and .[0].head.sha == $sha then .[0]
      else error("Managed version PR identity/head changed") end')"
}

report() {
  printf '%s\n' "$1"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$1" >> "$GITHUB_STEP_SUMMARY"
  fi
}

require_head
pr_number="$(printf '%s' "$pr" | jq -er '.number')"
report "## Version PR #$pr_number — checks for $head_sha"
report "PR: https://github.com/$repository/pull/$pr_number"
report 'This step schedules checks only. Merge still requires actual CI, CodeQL and code-scanning protection to pass.'

for workflow in ci.yml codeql.yml; do
  # Briefly allow GitHub to register the automatic PR event before falling
  # back to dispatch. API failure is an error, never evidence of a missing run.
  for attempt in 1 2 3; do
    run_pages="$(gh api --paginate --slurp \
      "repos/$repository/actions/workflows/$workflow/runs?head_sha=$head_sha&branch=$branch&per_page=100")"
    existing="$(printf '%s' "$run_pages" |
      jq -ce --arg sha "$head_sha" --arg branch "$branch" --arg repo "$repository" --arg path ".github/workflows/$workflow" '
        [.[] | .workflow_runs[] | select(.head_sha == $sha and .head_branch == $branch and
          .head_repository.full_name == $repo and .path == $path and
          (.event == "pull_request" or .event == "workflow_dispatch"))] |
        # PR checks include GitHub code-scanning PR context; prefer them even
        # when an older dispatched run already succeeded on this same SHA.
        ([.[] | select(.event == "pull_request")] | sort_by(.run_number) | last) //
        (sort_by(.run_number) | last) // {}')"
    run_id="$(printf '%s' "$existing" | jq -r '.id // empty')"
    if [ -n "$run_id" ] || [ "$attempt" = 3 ]; then break; fi
    sleep 2
  done

  require_head
  if [ -n "$run_id" ]; then
    state="$(printf '%s' "$existing" | jq -r '.conclusion // .status')"
    report "- $workflow: $state — https://github.com/$repository/actions/runs/$run_id (existing run; no duplicate dispatch)."
    case "$state" in
      action_required)
        echo "::notice::Approve workflows to run on version PR #$pr_number; $workflow was not dispatched again."
        report '  A maintainer must select **Approve workflows to run** on the PR. Approval is not automated or bypassed.' ;;
      success|queued|in_progress|waiting|pending|requested) ;;
      *)
        echo "::warning::$workflow needs operator attention; inspect the existing exact-head run rather than dispatching a replacement."
        report '  Inspect the failed/cancelled run and explicitly fix or rerun it. No automatic retry hides its result.' ;;
    esac
  else
    # The release workflow serializes this helper. A repeated invocation finds
    # this exact-head dispatch instead of blindly creating another full run.
    gh workflow run "$workflow" --repo "$repository" --ref "$branch"
    require_head
    report "- $workflow: no exact-head PR/dispatch run was registered; dispatched the existing required workflow once."
  fi
done
