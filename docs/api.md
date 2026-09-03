# Center API v1

The canonical machine-readable contract is [`openapi.json`](openapi.json). It is OpenAPI 3.1 and covers every method and path registered below `/api/v1` by Center.

Authentication is divided into four explicit audiences:

- **Browser administrator reads** require the `vastora_session` SameSite cookie.
- **Browser administrator mutations** require that cookie and the matching `X-CSRF-Token` header.
- **Agent control-plane calls** require the bearer credential bound to the Agent id in the path.
- **Bootstrap calls** are unauthenticated unless the operation declares a one-time enrollment bearer token. `GET /api/v1/setup/status` optionally accepts an administrator session and returns only non-sensitive bootstrap data otherwise.

JSON request bodies must use `application/json`, contain exactly one value, remain at or below 1 MiB, and contain no unknown fields. General errors use the JSON shape `{ "code": "invalid_request", "error": "..." }`. Login failures additionally return `retryAfterSeconds` and `captchaRequired`; throttled responses also set the standard `Retry-After` header. The contract declares binary downloads and server-sent event streams separately from JSON responses.

Regenerate the checked-in document after changing Center routes:

```sh
node scripts/generate-openapi.mjs
```

`go test ./internal/center` validates the document with `kin-openapi`, compares its method/path/security set with the Go route registry, and checks representative bootstrap, administrator, Agent, Catalog, publication, update, and integration behavior. `node scripts/generate-openapi.mjs --check` additionally verifies that the generated document is current.
