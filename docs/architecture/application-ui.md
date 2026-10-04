# Application interface ownership

Center provides the install, update, access publication, status, and removal
controls for every application. It also renders management forms for third-party
applications from their declarative catalog metadata. Catalog data does not
provide arbitrary JavaScript, CSS, or URL-based management views.

Vastora's official applications own their product interfaces:

- Meridian owns its node, network, landing, account, and subscription pages in
  `petauron/meridian/ui`. Center passes authenticated platform data and actions
  through the versioned mount API. Center does not compile Meridian pages into
  its frontend.
- Pulse serves its authenticated dashboard from the Pulse application. Center
  shows installation and access status, then opens the configured private HTTPS
  entry. The Pulse dashboard is not mirrored in Center.

The exact official identities are `vastora-official/meridian` and
`vastora-official/pulse`. A third-party app with the same short name follows the
declarative Center path. Center retains all authorization checks on API calls;
loading an official interface does not grant any new permission.

## Meridian release and trust boundary

The Meridian UI produces a JS module and stylesheet named
`ui-meridian-<application-version>.js` and `.css`. Its reviewed source commit is
an input to the protected official catalog publication workflow. That workflow
builds it against the pinned Center platform UI source, checks the resulting
bytes before loading publication credentials, and signs both files as TUF targets
beside `stable.json`. An application version cannot be reused with different UI
bytes. To change the UI, release a new Meridian application version.

Center's official catalog refresh verifies the JS and CSS target bytes through
the same TUF root used for official application manifests. Schema 101 stores the
verified pair and their hashes in a transaction with the accepted catalog and
records immutable hashes by application version. The authenticated
`/api/v1/official-app-ui/meridian/<version>/bundle.js` and `bundle.css` routes
serve only the verified pair. If either file or its accepted catalog is absent,
the workspace displays an error instead of falling back to bundled pages.
Center still exposes installation status, upgrade, and installation management
for affected Meridian instances so an unavailable UI bundle cannot hide the
platform recovery controls.

The module exports `apiVersion = 1`, `mount`, and `mountManager`; each mount
returns `update` and `unmount`. Center controls the mount lifecycle and passes
the current application data, language, and platform callbacks. The workspace
uses the accepted catalog's current Meridian UI version for the whole installed
group; individual nodes may still show older installed runtime versions until
upgraded. The module runs
as trusted first-party code under Center's existing session and content security
policy. Do not add third-party script URLs to this path.

## Rollout

1. Review and merge the Meridian UI source, then record its immutable full
   commit SHA. Its CI must type-check against the pinned Center UI contract and
   build the matching JS/CSS pair.
2. Review and merge the Center schema, trusted asset cache, API, and frontend
   host. Rehearse the forward schema 108→109 migration against a copy of the
   released Center database; a migration failure stops startup and does not downgrade
   automatically.
3. Publish a new official catalog revision with the reviewed Meridian commit.
   The protected workflow rebuilds the UI and signs the exact bytes. A plain
   catalog refresh or Center release without these targets does not make the
   Meridian workspace available.
4. Back up A1's released Center database, publish the Center release, and use
   the managed update path. Center migrates to schema 109 on startup; stop the
   rollout if migration or health checks fail.
5. Refresh the trusted official catalog in the updated Center, then inspect
   Meridian and Pulse through an authenticated session.

Existing report, quality score, bandwidth, and Meridian command APIs are not
converted by this interface migration.
