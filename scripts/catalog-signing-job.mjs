#!/usr/bin/env node
// Materialize only role keys provided by the protected environment. Never
// generate a root, print keys, persist them in the ledger, or upload them.
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { spawnSync } from "node:child_process";
import path from "node:path";

const directory = process.env.CATALOG_KEY_DIRECTORY;
let created = false;
try {
  if (!directory || !process.env.RUNNER_TEMP || path.dirname(directory) !== process.env.RUNNER_TEMP) throw new Error("Invalid protected key directory");
  const signers = JSON.parse(process.env.CATALOG_SIGNERS);
  delete process.env.CATALOG_SIGNERS;
  if (Object.keys(signers).sort().join(",") !== "snapshot,targets,timestamp") throw new Error("Only online publication roles are permitted");
  mkdirSync(directory, { mode: 0o700 });
  created = true;
  for (const role of ["targets", "snapshot", "timestamp"]) {
    if (!Array.isArray(signers[role]) || signers[role].length < 1 || signers[role].length > 10) throw new Error("Missing role signer");
    process.env[`CATALOG_${role.toUpperCase()}_KEY_FILE`] = signers[role].map((pem, i) => {
      if (typeof pem !== "string" || pem.length > 8192 || !pem.startsWith("-----BEGIN PRIVATE KEY-----")) throw new Error("Invalid signer format");
      const location = path.join(directory, `${role}-${i}.pem`);
      writeFileSync(location, pem, { mode: 0o600, flag: "wx" });
      return location;
    }).join(",");
    signers[role] = [];
  }
  const result = spawnSync(process.execPath, ["scripts/publish-catalog.mjs"], { stdio: "inherit", env: process.env });
  if (result.status !== 0 || result.error) throw new Error("Publication failed");
} catch {
  console.error("Protected catalog signing job did not complete; no key material is included in diagnostics.");
  process.exitCode = 1;
} finally {
  delete process.env.CATALOG_SIGNERS;
  if (created) rmSync(directory, { recursive: true, force: true });
}
