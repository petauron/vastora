# Privacy checks

Vastora's public repository uses synthetic infrastructure identities in source,
tests, documentation, and examples. Real deployment addresses, private domains,
account identifiers, node labels, and runtime data must not be committed.

Run the repository check with:

```sh
make privacy-check
```

Install the repository-managed Git hooks with:

```sh
make hooks-install
```

The pre-commit hook scans the complete staged index. The pre-push hook scans
every file added or changed by commits that are not present on a remote-tracking
branch. CI scans the complete tracked worktree and blocks the required gate when
the policy fails.

The checker permits RFC documentation addresses and domains, private and
reserved network ranges, and a narrow list of reviewed public infrastructure
references in `scripts/privacy-policy.json`. Every public exception must include
a reason. Do not add customer or deployment-specific data to that allowlist.

Findings report only a category, source location, and one-way fingerprint. Raw
matched values are deliberately omitted so terminal and Actions logs do not
become a second disclosure channel.

This check complements GitHub Secret Scanning and Gitleaks. It cannot determine
whether every arbitrary human-readable label is private, so contributors must
still use neutral fixtures such as `Node A`, `Provider A`, and `Example Site`.
