import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { uploadCatalog } from "./upload-catalog-r2.mjs";

function fixture(t) {
  const directory = mkdtempSync(path.join(tmpdir(), "catalog-upload-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(path.join(directory, "targets"));
  for (const name of ["1.root.json", "1.targets.json", "1.snapshot.json", "timestamp.json", `targets/${"a".repeat(64)}.stable.json`, "publication-state.json", "manifest-history.json"]) writeFileSync(path.join(directory, name), JSON.stringify({ fixture: name }));
  const calls = [];
  const objects = new Map();
  const run = (_, args) => {
    const operation = args[5];
    const key = args[args.indexOf("--key") + 1];
    calls.push({ operation, key, args });
    assert.ok(key.startsWith("vastora/catalog/"));
    if (operation === "get-object") {
      if (!objects.has(key)) throw new Error("missing");
      writeFileSync(args[args.indexOf("--key") + 2], objects.get(key));
    } else {
      assert.equal(operation, "put-object");
      if (args.includes("--if-none-match") && objects.has(key)) throw new Error("precondition");
      if (args.includes("--if-match")) throw new Error("simulated concurrent activation");
      objects.set(key, readFileSync(args[args.indexOf("--body") + 1]));
    }
    return Buffer.from("{}");
  };
  return { options: { directory, bucket: "download", endpoint: "https://example.r2.cloudflarestorage.com", bootstrap: true }, run, calls, objects };
}

test("immutable files are verified before timestamp; ledger metadata is not uploaded to R2", t => {
  const f = fixture(t);
  uploadCatalog(f.options, f.run);
  assert.equal(f.calls.at(-1).key, "vastora/catalog/timestamp.json");
  assert.equal(f.calls.at(-1).operation, "get-object");
  assert.ok(f.calls.at(-2).args.includes("--if-none-match"));
  assert.equal(f.objects.size, 5);
  assert.ok(!f.calls.some(call => call.key.includes("publication-state")));
  assert.ok(!f.calls.some(call => call.key.includes("manifest-history")));
});

test("conflicting immutable bytes prevent activation", t => {
  const f = fixture(t);
  f.objects.set("vastora/catalog/1.root.json", Buffer.from("different"));
  assert.throws(() => uploadCatalog(f.options, f.run), /conflicts/);
  assert.ok(!f.calls.some(call => call.key.endsWith("timestamp.json")));
});

test("same immutable bytes are idempotent but activation conflict is never bypassed", t => {
  const f = fixture(t);
  uploadCatalog(f.options, f.run);
  f.calls.length = 0;
  assert.throws(() => uploadCatalog({ ...f.options, bootstrap: false, previousETag: '"previous"' }, f.run), /concurrent/);
  const activations = f.calls.filter(call => call.key.endsWith("timestamp.json"));
  assert.equal(activations.length, 1);
  assert.ok(activations[0].args.includes("--if-match"));
});

test("unexpected local file is rejected before any network operation", t => {
  const f = fixture(t);
  writeFileSync(path.join(f.options.directory, "signer.pem"), "private");
  assert.throws(() => uploadCatalog(f.options, f.run), /Unexpected/);
  assert.equal(f.calls.length, 0);
});

test("activation readback mismatch is reported without undoing another publisher", t => {
  const f = fixture(t);
  const run = (command, args) => {
    const value = f.run(command, args);
    if (args[5] === "put-object" && args.includes("vastora/catalog/timestamp.json")) f.objects.set("vastora/catalog/timestamp.json", Buffer.from("another publication"));
    return value;
  };
  assert.throws(() => uploadCatalog(f.options, run), /not confirmed/);
  assert.equal(f.calls.filter(call => call.operation === "put-object" && call.key.endsWith("timestamp.json")).length, 1);
});
