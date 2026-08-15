# Catalog format

One online source serves one signed JSON envelope:

```json
{
  "schemaVersion": 1,
  "keyId": "catalog-key-1",
  "payload": "base64url-exact-catalog-bytes",
  "signature": "base64url-ed25519-signature"
}
```

The signature is over the decoded `payload` bytes exactly. A source is locally
identified by a stable `id`; installed app identities are always
`source-id/app-id`, which prevents a private source from silently replacing an
official app.

## Private source setup

1. Maintain a regular catalog JSON using [the schema](../schemas/catalog.schema.json).
2. Generate an Ed25519 key pair with `vastora catalog keygen` and protect the
   generated private file.
3. Validate and sign the document with `vastora catalog validate` and
   `vastora catalog sign`.
4. Serve the resulting envelope from an HTTPS URL. Add the URL, the public key,
   an optional private CA, and an optional Bearer token in Vastora.

Catalog authentication and OCI registry credentials are intentionally separate.
The catalog never contains registry passwords, cloud keys, or application
secrets.

## App manifest contract

The catalog payload validates against the JSON Schema 2020-12 files in
[`schemas/`](../schemas/). Every app version declares a fixed OCI digest,
static Compose text, bilingual labels, and typed installation fields. A secret
field cannot provide a default. Conditions use a single field equality check;
there is no expression language and no app-provided shell hook.

The current repository validates and displays this contract. The future Node
deployment slice will implement the declarative delivery mapping and Compose
rendering; it will not execute arbitrary manifest code.

## Refresh behavior

Vastora accepts only HTTPS source URLs, limits an envelope to 5 MiB, sends
conditional HTTP requests, and removes the Bearer header on cross-origin
redirects. It verifies the signature and validates the payload before replacing
the cache. A failed refresh leaves the last verified catalog available and
shows the error to the administrator.
