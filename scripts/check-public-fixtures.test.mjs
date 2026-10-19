import assert from "node:assert/strict";
import test from "node:test";

import { formatViolation, scanContent } from "./check-public-fixtures.mjs";

test("accepts reserved documentation identities", () => {
  assert.deepEqual(scanContent("fixture.test.ts", Buffer.from('const fixture = "https://node-a.example.com/203.0.113.10";')), []);
});

test("classifies mapped documentation addresses without truncating IPv6", () => {
  for (const address of ["::ffff:203.0.113.1", "::ffff:c000:201", "0:0:0:0:0:ffff:198.51.100.1"]) {
    assert.deepEqual(scanContent("fixture.test.ts", Buffer.from(`const mapped = "${address}";`)), []);
  }
});

test("still rejects routable IPv4-mapped addresses without printing them", () => {
  // Same synthetic routable range as the existing rejection fixture below.
  const raw = `::ffff:${[198, 52, 100, 42].join(".")}`;
  const findings = scanContent("fixture.test.ts", Buffer.from(`const mapped = "${raw}";`));
  assert.ok(findings.some((finding) => finding.kind === "public-ipv6"));
  assert.ok(findings.every((finding) => !formatViolation(finding).includes(raw)));
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

test("permits only the reviewed full mapped resolver fixture, not truncated tokens", () => {
  assert.deepEqual(scanContent("fixture.test.ts", Buffer.from('const fixture = "::ffff:1.1.1.1";')), []);
});

test("detects a blocked identifier by fingerprint", () => {
  const raw = ["Cloud", "lead"].join("");
  const findings = scanContent("fixture.test.ts", Buffer.from(`const name = "${raw}";`));
  assert.equal(findings.length, 1);
  assert.equal(findings[0].kind, "blocked-identifier");
  assert.ok(!formatViolation(findings[0]).includes(raw));
});
