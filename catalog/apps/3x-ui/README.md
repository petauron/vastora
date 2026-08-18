# 3x-ui app package

This package pins the official `ghcr.io/mhsanaei/3x-ui` v3.6.0 image by
content digest. Its typed Agent executor follows the upstream storage layout:

- `db` persists the default SQLite state at `/etc/x-ui`.
- `cert` persists panel certificates.
- `acme` persists ACME account and renewal state.

The management panel and subscription server are bound to the Agent's
Center-confirmed private service address. Publish either service through an
explicit Vastora access point; the management panel is never public by default.
The package grants `NET_ADMIN` and `NET_RAW` because upstream enables Fail2ban
by default to enforce per-client IP limits.

The Vastora Agent installs this package through an explicit typed handler. It
does not expose arbitrary Docker, Compose, or shell execution.
