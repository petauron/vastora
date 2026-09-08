# Public error and sign-in disclosure review

Source review: 2026-09-07. No local tests, builds, linters, type checks, browser
QA, production access or deployment were performed for this change. The added
regression cases are not presented as executed validation.

## Changes

- Sign-in displays ordinary recovery guidance, not failure thresholds, lockout
  duration, increasing-delay rules or a retry countdown. Setup no longer
  advertises an implementation-specific bootstrap-token statement.
- Login responses and their API contract no longer carry retry durations or a
  `Retry-After` header. The setup login-protection object contains only the
  challenge requirement and, when needed, its public site key. Server-side
  throttling, lockout, challenge verification and credential verification remain
  unchanged. Removing client countdown logic does not remove server enforcement.
- Anonymous setup/status responses do not disclose the suggested Agent URL or
  installed infrastructure capabilities. These fields remain empty/false until
  an administrator authenticates; normal authenticated onboarding retains them.
- General Center HTTP errors now use allowlisted messages and stable error
  categories instead of serializing `err.Error()`. Malformed JSON, database
  paths, upstream response bodies and session-failure details no longer appear
  in those responses. Authentication and internal-error categories do not reveal
  the internal subsystem that failed.
- Public health/readiness endpoints retain their status codes and status values
  but no longer include the Center version. Authenticated system status still
  supplies the version needed by the management interface.
- Production UI error handling no longer explicitly logs caught exceptions and
  component stacks. Development diagnostics are unchanged. This does not claim
  control over every browser/framework-generated console message.
- The 3x-ui client and node-traffic forms use the existing localized error mapper
  instead of printing arbitrary task/API error text as their primary message.

## Other surfaces reviewed

The Center route registry protects diagnostics, task events, application state,
network configuration and application credential management with administrator
session checks. Mutations additionally require CSRF validation. Technical
details in those authenticated views are not equivalent to disclosure on the
anonymous sign-in page; they remain available for operator troubleshooting.
This source review is not proof that every possible path is vulnerability-free.

Installer scripts, the official catalog, static frontend assets and the public
Turnstile site key have intentional public audiences. They are not credentials.
Enrollment, Agent control-plane calls and decommission callbacks use their own
credential boundaries. Do not remove these interfaces simply to conceal product
implementation details.

Vastora is open source. These changes reduce unnecessary runtime disclosure;
they do not make the authentication design secret or replace its actual security
controls. This follows the separation between public generic errors and internal
diagnostics described in the [OWASP Error Handling Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Error_Handling_Cheat_Sheet.html).

## Regression coverage added or updated (not run locally)

- Generic HTTP error payloads do not contain supplied internal markers, database
  paths, upstream secrets or expired-session details; responses stay no-store.
- Public health responses contain no version while readiness still gates startup.
- Anonymous setup hides infrastructure and login policy; authenticated setup
  retains the operator's configuration.
- Login is still throttled server-side, even though the countdown is absent;
  correct credentials work after the enforced delay expires.
- Protected sign-in still submits and resets the challenge, preserves inline
  accessible error messages, and displays generic throttling/unavailability
  feedback in both supported languages.
