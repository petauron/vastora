# Contributing to Vastora

Thank you for helping build a small, auditable control plane.

## Contribution rules

- Open an issue with an outcome, non-goals, interface impact, acceptance
  criteria, security notes, and tests before starting a non-trivial change.
- Keep each pull request as one complete and runnable vertical slice.
- Do not add placeholder handlers, inert controls, unused abstractions, real
  infrastructure identifiers, credentials, or private runtime data.
- Run `make check` and `make security-check` before opening a pull request.
- Sign commits off under the Developer Certificate of Origin.

## Local development

Use Go 1.26 and Node.js 24, then run `make bootstrap`. The test suite uses
temporary SQLite databases and local TLS servers only; it must not require a
Docker daemon, a registry account, or an external control plane.

The web build uses the TypeScript 7 native compiler. Until TypeScript 7 exposes
the programmatic API required by `typescript-eslint`, the `typescript` package
name is an npm alias for the TypeScript 6 compatibility API and
`@typescript/native` supplies the TypeScript 7 `tsc` executable. Keep both
entries together when updating the TypeScript toolchain.

## Translation

English is the source language and Simplified Chinese ships with every visible
v0.1 interface. Add both translations in the same pull request.
