# Application-owned workspaces

Meridian owns its presentation in `web/src/app-modules/meridian`:

- `manifest.ts`: typed UI manifest, page identities, localized navigation labels,
  surfaces, and shared data capabilities.
- `Workspace.tsx`: node overview and page composition.
- `NetworkMatrix.tsx`, `LinkBandwidth.tsx`: route comparisons and bandwidth UI.
- `Manager.tsx`: accounts, subscriptions, and Meridian domain operations.

Vastora owns `web/src/app-workspaces`: the module contract, reviewed registration,
shared provider host and lazy-loading surfaces. `InstalledApps` selects the exact
registered application identity; it does not define Meridian tabs or its account
manager. Generic installation, update, publication, and status controls remain
platform components. The existing legacy cutover action explicitly targets the
Meridian manager while passing the legacy installation; it is not an app-key
alias or alternate Meridian implementation.

## Trust and authorization

This release supports bundled first-party UI modules. Registration binds the
full source/app key to a build-time import. Catalog text cannot supply JavaScript
URLs, arbitrary import paths, or additional permissions. A third-party app with
the same short name cannot select the official module.

Declared capabilities select shared data providers. They are not authorization
grants. Center's existing authenticated administrative endpoints continue to
validate operations and application state. No endpoint permissions, session
scope, credentials or production routing policy change in this refactor.

## Manifest and release boundary

The UI manifest is part of the reviewed application frontend source and ships
with Center. The signed runtime package manifest is immutable and still owns
installation images, services and configuration; this change does not rewrite
an existing signed package version. UI modules are code-split but are not
independently released plugins. Independent UI distribution requires an explicit
signed asset and version contract; it is not implied by lazy loading.

No schema migration or data conversion is required. Scores, unlock evidence,
per-pair bandwidth reports and existing commands retain their current APIs.
