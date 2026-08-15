# Security policy

Do not disclose vulnerabilities through public issues. Use GitHub's private
security advisory flow for `petauron/vastora` when the repository exists.

Until that flow is enabled, report only a minimal, non-sensitive summary in a
private channel with the maintainer. Never include secrets, private hostnames,
addresses, access tokens, or proof-of-concept payloads that target real hosts.

## Supported versions

Before the first stable release, only the default branch receives security
updates. Pre-alpha builds are not production-supported.

## Security invariants

- Catalogs must be signed before use.
- Secret values are write-only API inputs and must never appear in responses,
  task payload logs, catalog files, Git history, or UI state.
- Nodes are the only components allowed to access Docker sockets.
- Runtime application data remains outside Master configuration storage.
