import assert from "node:assert/strict";
import test from "node:test";

import { formatViolation, scanContent } from "./check-public-fixtures.mjs";

test("accepts reserved documentation identities", () => {
  assert.deepEqual(scanContent("fixture.test.ts", Buffer.from('const fixture = "https://node-a.example.com/203.0.113.10";')), []);
});

test("rejects a routable address without printing it", () => {
  const raw = [198, 52, 100, 42].join(".");
  const findings = scanContent("fixture.test.ts", Buffer.from(`const address = "${raw}";`));
  assert.equal(findings.length, 1);
  assert.equal(findings[0].kind, "public-ipv4");
  assert.doesNotMatch(formatViolation(findings[0]), new RegExp(raw.replaceAll(".", "\\.")));
});

test("rejects an unapproved hostname without printing it", () => {
  const raw = ["private", "customer", "top"].join(".");
  const findings = scanContent("fixture.test.ts", Buffer.from(`const hostname = "${raw}";`));
  assert.equal(findings.length, 1);
  assert.equal(findings[0].kind, "unapproved-domain");
  assert.ok(!formatViolation(findings[0]).includes(raw));
});

test("permits reviewed public infrastructure references", () => {
  assert.deepEqual(scanContent("fixture.test.ts", Buffer.from('const url = "https://api.github.com/repos"; const dns = "1.1.1.1";')), []);
});

test("detects a blocked identifier by fingerprint", () => {
  const raw = ["Cloud", "lead"].join("");
  const findings = scanContent("fixture.test.ts", Buffer.from(`const name = "${raw}";`));
  assert.equal(findings.length, 1);
  assert.equal(findings[0].kind, "blocked-identifier");
  assert.ok(!formatViolation(findings[0]).includes(raw));
});
