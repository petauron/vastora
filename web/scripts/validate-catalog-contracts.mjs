import { Buffer } from "node:buffer";
import { createPublicKey, verify } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const schemaDirectory = join(repositoryRoot, "schemas");
const fixtureDirectory = join(repositoryRoot, "internal", "catalog", "testdata", "v3");

const readJSON = async (path) => JSON.parse(await readFile(path, "utf8"));
const appSchema = await readJSON(join(schemaDirectory, "app-manifest.schema.json"));
const catalogSchema = await readJSON(join(schemaDirectory, "catalog.schema.json"));
const envelopeSchema = await readJSON(join(schemaDirectory, "catalog-envelope.schema.json"));
const contractCases = await readJSON(join(fixtureDirectory, "contract-cases.json"));

const ajv = new Ajv2020({ allErrors: true, strict: true, strictRequired: false });
addFormats(ajv);
ajv.addSchema(appSchema);
const validateCatalogSchema = ajv.compile(catalogSchema);
const validateEnvelopeSchema = ajv.compile(envelopeSchema);

function requireUnique(values, kind) {
  if (new Set(values).size !== values.length) {
    throw new Error(`duplicate ${kind}`);
  }
}

function validateCatalogSemantics(catalog) {
  requireUnique(catalog.apps.map((app) => app.id), "app id");
  for (const app of catalog.apps) {
    requireUnique((app.images ?? []).map((image) => image.name), `image id in ${app.id}`);
    requireUnique(app.config.map((field) => field.key), `config id in ${app.id}`);
    requireUnique((app.services ?? []).map((service) => service.name), `service id in ${app.id}`);
    requireUnique(
      (app.artifacts ?? []).map((artifact) => `${artifact.name}\0${artifact.operatingSystem}\0${artifact.architecture}`),
      `artifact target in ${app.id}`,
    );
    const fields = new Map(app.config.map((field) => [field.key, field.type]));
    const services = new Set((app.services ?? []).map((service) => service.name));
    for (const service of app.services ?? []) {
      if (service.hostPortField) {
        if (!fields.has(service.hostPortField)) {
          throw new Error(`unknown host port field in ${app.id}`);
        }
        if (fields.get(service.hostPortField) !== "integer") {
          throw new Error(`non-integer host port field in ${app.id}`);
        }
      }
    }
    if (app.homepage && !services.has(app.homepage.service)) {
      throw new Error(`unknown homepage service in ${app.id}`);
    }
  }
}

function validateCatalog(catalog) {
  if (!validateCatalogSchema(catalog)) {
    throw new Error(ajv.errorsText(validateCatalogSchema.errors));
  }
  validateCatalogSemantics(catalog);
}

function expectAcceptance(kind, test, action) {
  let accepted = true;
  try {
    action();
  } catch {
    accepted = false;
  }
  if (accepted !== test.valid) {
    throw new Error(`${kind} ${JSON.stringify(test.value)} accepted=${accepted}, want ${test.valid}`);
  }
}

const validCatalogPath = join(fixtureDirectory, "valid-catalog.json");
const validCatalogBytes = await readFile(validCatalogPath);
const validCatalog = JSON.parse(validCatalogBytes);
validateCatalog(validCatalog);
for (const test of contractCases.versions) {
  expectAcceptance("version", test, () => {
    const value = structuredClone(validCatalog);
    value.apps[0].version = test.value;
    validateCatalog(value);
  });
}
for (const test of contractCases.imageReferences) {
  expectAcceptance("image reference", test, () => {
    const value = structuredClone(validCatalog);
    value.apps[0].images[0].reference = test.value;
    validateCatalog(value);
  });
}
for (const test of contractCases.generatedAt) {
  expectAcceptance("generatedAt", test, () => {
    const value = structuredClone(validCatalog);
    value.generatedAt = test.value;
    validateCatalog(value);
  });
}
const additionalValidDirectory = join(fixtureDirectory, "valid");
const additionalValidFixtures = (await readdir(additionalValidDirectory)).filter((name) => name.endsWith(".json")).sort();
if (additionalValidFixtures.length === 0) {
  throw new Error("additional valid catalog fixtures are missing");
}
for (const fixture of additionalValidFixtures) {
  validateCatalog(await readJSON(join(additionalValidDirectory, fixture)));
}
validateCatalog(await readJSON(join(repositoryRoot, "catalog", "catalog.json")));

const invalidDirectory = join(fixtureDirectory, "invalid");
const invalidFixtures = (await readdir(invalidDirectory)).filter((name) => name.endsWith(".json")).sort();
if (invalidFixtures.length === 0) {
  throw new Error("invalid catalog fixtures are missing");
}
for (const fixture of invalidFixtures) {
  const catalog = await readJSON(join(invalidDirectory, fixture));
  let rejected = false;
  try {
    validateCatalog(catalog);
  } catch {
    rejected = true;
  }
  if (!rejected) {
    throw new Error(`invalid fixture was accepted: ${fixture}`);
  }
}

const envelope = await readJSON(join(fixtureDirectory, "valid-envelope.json"));
if (!validateEnvelopeSchema(envelope)) {
  throw new Error(ajv.errorsText(validateEnvelopeSchema.errors));
}
for (const test of contractCases.keyIds) {
  expectAcceptance("keyId", test, () => {
    const value = structuredClone(envelope);
    value.keyId = test.value;
    if (!validateEnvelopeSchema(value)) {
      throw new Error(ajv.errorsText(validateEnvelopeSchema.errors));
    }
  });
}
const payload = Buffer.from(envelope.payload, "base64url");
if (!payload.equals(validCatalogBytes)) {
  throw new Error("envelope payload is not the exact valid catalog fixture bytes");
}
validateCatalog(JSON.parse(payload));

const rawPublicKey = Buffer.from((await readFile(join(fixtureDirectory, "catalog-signing-public.key"), "utf8")).trim(), "base64url");
if (rawPublicKey.length !== 32) {
  throw new Error("fixture public key is not a raw Ed25519 public key");
}
const ed25519SPKIPrefix = Buffer.from("302a300506032b6570032100", "hex");
const publicKey = createPublicKey({ key: Buffer.concat([ed25519SPKIPrefix, rawPublicKey]), format: "der", type: "spki" });
const signature = Buffer.from(envelope.signature, "base64url");
if (!verify(null, payload, publicKey, signature)) {
  throw new Error("portable fixture signature is invalid");
}

const tamperedPayload = Buffer.from(payload);
tamperedPayload[tamperedPayload.length - 2] ^= 1;
if (verify(null, tamperedPayload, publicKey, signature)) {
  throw new Error("changing one payload byte did not invalidate the signature");
}

console.log(
  `validated catalog v0.1 contracts: ${1 + additionalValidFixtures.length} valid, ${invalidFixtures.length} invalid, ${Object.values(contractCases).flat().length} boundary cases, exact-byte signature and tamper case`,
);
