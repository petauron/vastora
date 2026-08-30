# Catalog v0.1 contract

Vastora accepts one portable, signed Catalog envelope from each online source.
The v0.1 contract is published as three JSON Schema 2020-12 documents:

- [`catalog-envelope.schema.json`](../schemas/catalog-envelope.schema.json)
- [`catalog.schema.json`](../schemas/catalog.schema.json)
- [`app-manifest.schema.json`](../schemas/app-manifest.schema.json)

The JSON `schemaVersion` is `3`. Vastora is still prerelease software, so a
future incompatible Catalog contract will replace this version outright rather
than adding aliases or a compatibility parser.

## Identity and executor boundary

A source has one stable lowercase `source-id`. An app has one lowercase
`app-id` and one semantic `version`; the deployable identity is therefore
`source-id/app-id + version`. A source must never publish different content for
an identity it has already published. Center-side immutable-version enforcement
is tracked separately from this portable format.

The manifest describes typed configuration, digest-pinned OCI images,
SHA-256-pinned native artifacts, addressable services, health paths, and whether
the built-in executor needs host access. It does not carry Compose, shell
commands, environment/file templates, arbitrary host paths, install profiles,
or a generic permission language. An Agent executes only a known typed executor
for an explicitly supported app key. `hostAccess` is a disclosure shown to the
administrator, not permission to run arbitrary catalog content.

Application services are private after installation. LAN, secure-private, and
public access are separate Publication resources. Uninstall keeps application
data unless the administrator explicitly requests deletion; each typed executor
owns the exact volumes or files it is allowed to remove.

## Exact-byte envelope

The transport representation is one JSON object:

```json
{
  "schemaVersion": 3,
  "keyId": "catalog-key-1",
  "payload": "base64url-exact-catalog-bytes",
  "signature": "base64url-ed25519-signature"
}
```

`payload` is unpadded RFC 4648 base64url. `signature` is the unpadded base64url
encoding of the 64-byte Ed25519 signature over the decoded payload bytes
exactly. Whitespace, line endings, and property order are therefore signed; a
consumer must not parse and reserialize the payload before verification.

Public and private key files contain one unpadded base64url line. The decoded
public key is the raw 32-byte Ed25519 public key and the decoded private key is
the raw 64-byte Ed25519 private key. `keyId` selects an operator-configured
public key; it is not a key or fingerprint itself. Center accepts public keys
only and never accepts a catalog private key through its API.

## CLI workflow

The complete portable workflow uses ordinary files:

```sh
vastora catalog keygen --out-dir ./catalog-keys
vastora catalog validate --catalog ./catalog.json
vastora catalog sign \
  --catalog ./catalog.json \
  --private-key ./catalog-keys/catalog-signing-private.key \
  --key-id catalog-key-1 \
  --output ./catalog-envelope.json
vastora catalog verify \
  --envelope ./catalog-envelope.json \
  --public-key ./catalog-keys/catalog-signing-public.key
```

`keygen` creates a new directory if needed, writes the private key with mode
`0600`, and writes the public key with mode `0644`. It refuses to overwrite an
existing key file. `sign` refuses private keys that grant group or other access.
Standard output contains file paths and the public-key
fingerprint, never private key material. Validation and verification success
messages go to standard output. Flag usage and errors go to standard error. A
successful command exits `0`; an invalid contract, bad key, failed signature,
missing file, unsafe value, or invalid invocation exits non-zero.

## Validation rules

JSON Schema validation and Vastora's semantic validation are both required.
Schemas reject unknown fields, unsupported types, secret defaults, mutable
image references, unsafe artifact URLs, invalid health/homepage paths, and
unsupported permission or delivery fields. Semantic validation additionally
rejects duplicate app, image, config, service, and platform-artifact identities
and references to unknown fields or services.

Integer defaults use JSON numeric semantics and must be within the portable safe
range `-9007199254740991` through `9007199254740991`. Equivalent integral JSON
lexemes such as `1`, `1.0`, and `1e0` are accepted consistently by the Go
validator, Center deployment normalization, and independent JSON Schema
consumers; Center emits the canonical decimal integer to an Agent.

The portable fixtures in [`internal/catalog/testdata/v3`](../internal/catalog/testdata/v3)
contain synthetic values only. Go tests consume them through Vastora's parser;
an independent Node/Ajv consumer validates the same JSON Schemas, semantic
identity constraints, exact payload bytes, Ed25519 signature, and one-byte
tamper case without importing `internal/catalog`.

## Private source refresh

Serve the signed envelope from an HTTPS URL, then add that URL, its public key,
an optional private CA, and an optional Bearer token in Center. Catalog
authentication and OCI registry credentials are intentionally separate. The
Catalog never contains registry passwords, cloud keys, or application secrets.

Center limits an envelope to 5 MiB, sends conditional HTTP requests, and removes
the Bearer header on cross-origin redirects. It verifies the signature and
validates the payload before replacing the cache. A failed refresh leaves the
last verified Catalog available and reports the error to the administrator.
