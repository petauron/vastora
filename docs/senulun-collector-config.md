# Senlun package configuration

Senlun runs independently of Vastora. Vastora supplies validated package inputs,
keeps package secrets encrypted and manages installation resources and entries.
Accounts, tokens, collection intervals, probe tasks and monitoring data belong to
Senlun. There is no global monitoring Service or automatic Service-side pairing
in the new installation flow.

## Service

| Key | Meaning |
| --- | --- |
| `public_url` | Required exact HTTPS browser origin, for example `https://senulun.example.com`. No subpath, query, fragment or embedded credentials. A historical upgrade without this value may inherit its own existing ready HTTPS entry. |
| `trusted_proxy_ip` | Optional exact immediate proxy peer. Leave empty unless verified. Do not trust arbitrary client-supplied forwarding headers. |

Create the administrator on Senlun's own `/login` page using a private entry
before public exposure. Current Senlun does not use the historical `setup_token`;
new packages neither request it nor inject a bootstrap secret file. Historical
deployment evidence is not rewritten.

## Agent

| Key | Meaning |
| --- | --- |
| `service_url` | Required HTTPS Service root ending in `/`. May point to a Service outside Vastora; it must be reachable without interactive browser authentication. |
| `node_name` | Required display name chosen by the administrator. |
| `node_group` | Optional Senlun group. |
| `enrollment_token` | Required write-only secret for the initial install, created in Senlun. It is encrypted and delivered separately from public task configuration. |
| `node_region` | Optional ISO 3166-1 alpha-2 country code, such as `SG`. Takes precedence over country discovery. The API accepts an explicit empty string to clear it. |
| `geoip_provider` | `geojs` (default), `ipinfo` or `disabled`. |

The Agent writes its own per-node credential to persistent state. Upgrade and
configure operations preserve that identity and do not replay the initial token.
Changing a Service URL is not an automatic identity migration. Keep-data removal
preserves credentials; delete-data removal requires a fresh enrollment next time.

The default GeoJS request shares the public egress IP and Agent version with
GeoJS, never monitoring credentials or host metrics. Select `disabled` to opt out.
Existing explicit country/provider settings are retained during upgrades. No
collection interval is injected by Vastora.

## Ownership and rollout

Multiple hosts may each run a Service, and each Agent independently selects its
Service. Existing package identities and resource paths remain unchanged; no
container recreation, host rename or schema migration is triggered merely by
updating these sources or refreshing the catalog.

Deploy the compatible Center/Agent before publishing the revised signed recipes.
Recipe publication is separate from program release and does not upgrade running
applications. Drain historical pairing/recovery tasks before updating the manager;
their immutable evidence remains supported for existing installations. New Agent
recovery preserves its data without contacting or rotating credentials on a
Center-managed Service.

See the [standalone installation guide](https://github.com/petauron/senlun/blob/main/docs/STANDALONE.md)
for installation and operation without Vastora.
