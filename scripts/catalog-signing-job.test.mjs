import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./catalog-signing-job.mjs", import.meta.url));
// Deliberately not a key: this tests secret file handling, never cryptography.
const fakePEM = "-----BEGIN PRIVATE KEY-----\nfixture-not-key-material\n-----END PRIVATE KEY-----";

function fixture(t) {
  const directory = mkdtempSync(path.join(tmpdir(), "catalog-signing-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(path.join(directory, "scripts"));
  writeFileSync(path.join(directory, "scripts/publish-catalog.mjs"), `
    import { readFileSync, statSync, writeFileSync } from "node:fs";
    const files = ["TARGETS", "SNAPSHOT", "TIMESTAMP"].map(role => process.env["CATALOG_" + role + "_KEY_FILE"]);
    writeFileSync(process.env.OBSERVATION, JSON.stringify({
      rawSecretInherited: process.env.CATALOG_SIGNERS !== undefined,
      modes: files.map(file => statSync(file).mode & 0o777),
      nonempty: files.every(file => readFileSync(file).length > 0)
    }));
    process.exit(Number(process.env.CHILD_EXIT || 0));
  `);
  const keyDirectory = path.join(directory, "keys");
  const observation = path.join(directory, "observation.json");
  const signers = { targets: [fakePEM], snapshot: [fakePEM], timestamp: [fakePEM] };
  const run = (overrides = {}) => spawnSync(process.execPath, [script], { cwd: directory, encoding: "utf8", env: { PATH: process.env.PATH, RUNNER_TEMP: directory, CATALOG_KEY_DIRECTORY: keyDirectory, CATALOG_SIGNERS: JSON.stringify(signers), OBSERVATION: observation, ...overrides } });
  return { directory, keyDirectory, observation, signers, run };
}

test("online keys use owner-only temporary files and the raw secret never reaches the publisher", t => {
  const f = fixture(t);
  const result = f.run();
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(readFileSync(f.observation)), { rawSecretInherited: false, modes: [0o600, 0o600, 0o600], nonempty: true });
  assert.equal(existsSync(f.keyDirectory), false);
  assert.ok(!`${result.stdout}${result.stderr}`.includes("fixture-not-key-material"));
});

test("a failed publisher removes key files and only emits a generic signing error", t => {
  const f = fixture(t);
  const result = f.run({ CHILD_EXIT: "1" });
  assert.equal(result.status, 1);
  assert.equal(existsSync(f.keyDirectory), false);
  assert.match(result.stderr, /did not complete/);
  assert.ok(!result.stderr.includes(fakePEM));
});

test("offline root keys and unscoped directories are rejected before invoking the publisher", t => {
  const f = fixture(t);
  for (const overrides of [
    { CATALOG_SIGNERS: JSON.stringify({ ...f.signers, root: [fakePEM] }) },
    { CATALOG_KEY_DIRECTORY: f.directory },
    { CATALOG_SIGNERS: "malformed" },
  ]) {
    const result = f.run(overrides);
    assert.equal(result.status, 1);
    assert.equal(existsSync(f.observation), false);
    assert.equal(existsSync(f.keyDirectory), false);
  }
});

test("an existing key directory is never overwritten or cleaned by another run", t => {
  const f = fixture(t);
  mkdirSync(f.keyDirectory);
  const marker = path.join(f.keyDirectory, "existing.txt");
  writeFileSync(marker, "preserve existing work");
  assert.equal(f.run().status, 1);
  assert.equal(readFileSync(marker, "utf8"), "preserve existing work");
  assert.equal(existsSync(f.observation), false);
});
