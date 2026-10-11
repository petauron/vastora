# Glivela and Senulun brand migration

Meridian is now **Glivela**; Pulse is now **Senulun**. Vastora is unchanged.
This migration changes product labels, not installed application identity.

| Product | Permanent application key | State retained |
| --- | --- | --- |
| Glivela | `vastora-official/meridian` | Accounts, subscriptions, quotas, routes, credentials and runtime receipts |
| Senulun | `vastora-official/pulse` | Dashboard, settings, users, monitoring history and volumes |
| Senulun Agent | `vastora-official/pulse-agent` | Node identity, enrollment credentials and owned host resources |

Keep database table/JSON names, API routes, module paths, environment variables,
executables, units, persisted paths and signed asset names as stable technical
contracts. Do not add aliases, dual reads/writes, fallback routes or duplicate
apps merely to remove an old spelling. Human-facing names are separate from
machine identities. Do not rewrite signed historical catalogs or manifests.

Center migration 110 changes only exact old default application display names
under the three official keys. Custom application names, third-party apps,
service names, tasks, configuration, subscriptions, runtime generations and
catalog trust remain untouched. The existing migration runner backs up before
applying migrations, fails closed and does not support database downgrade.

The Senulun service has its own backed-up, forward-only default-site-name
migration. The catalog updates display metadata using a new package revision;
it does not change resource names, artifact coordinates or hashes. Changing a
catalog entry's label does not install, enroll, restart or upgrade an instance.

## Review and rollout order

1. Review the Glivela, Senulun, Vastora and catalog PRs together. PR creation is
   not a production migration. Leave source repositories at their current URLs
   while pinned consumers and Release Please still refer to those coordinates.
2. Merge approved source PRs and use their Release Please version PRs and real
   CI on the exact immutable commits. Do not manually bump versions or tags.
3. Before catalog publication, separately update the Glivela UI source pin and
   UI version to the new checked release. Publish Senulun through the existing
   verified release-source flow. Never replace assets at an old version.
4. Obtain explicit authorization for protected catalog signing/publication and
   for managed upgrades. Program release, catalog publication and instance
   upgrade are three separate operations.
5. Verify names, subscription behavior, collector node IDs, history, traffic
   totals and custom site titles after an authorized rollout. Do not create a
   second install. A repository rename requires a separate coordinated source,
   provenance and link update; it is not performed by these code changes.

Local tests/builds are not run unless explicitly requested. The PR workflows
run genuine checks; no required-check results are synthesized.
