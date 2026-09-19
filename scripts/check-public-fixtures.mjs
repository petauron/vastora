#!/usr/bin/env node

import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { isIP } from "node:net";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const projectDirectory = resolve(scriptDirectory, "..");
const policy = JSON.parse(readFileSync(resolve(scriptDirectory, "privacy-policy.json"), "utf8"));
const zeroOID = /^0+$/;
const ipv4Pattern = /(?<![0-9.])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?![0-9.])/g;
const ipv6Pattern = /(?<![0-9a-f:])(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4}(?![0-9a-f:])/gi;
const domainPattern = /(?<![a-z0-9_-])(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|dev|cloud|top|xyz|cn|co|me|tech|site|online|no|gov|edu|invalid|test)(?![a-z0-9_-])/gi;
const wordPattern = /[a-z0-9][a-z0-9._-]*/gi;
const allowedPublicIPv4 = new Set(policy.allowedPublicIPv4.map((entry) => entry.value));
const allowedPublicIPv6 = new Set(policy.allowedPublicIPv6.map((entry) => entry.value.toLowerCase()));
const allowedDomainRoots = policy.allowedDomainRoots.map((entry) => entry.value.toLowerCase());
const blockedFingerprints = new Set(policy.blockedFingerprints.map((entry) => entry.sha256));

function git(args, options = {}) {
  const execOptions = { cwd: projectDirectory, input: options.input };
  if (options.encoding) execOptions.encoding = options.encoding;
  return execFileSync("git", args, execOptions);
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function normalizeIdentifier(value) {
  return value.normalize("NFKC").toLowerCase().replace(/[-_|/]+/g, " ").replace(/[^\p{L}\p{N}. ]+/gu, " ").replace(/\s+/g, " ").trim();
}

function fingerprint(value) {
  return sha256(normalizeIdentifier(value));
}

function decodePaths(raw) {
  return raw.toString("utf8").split("\0").filter(Boolean);
}

function lineNumber(text, offset) {
  let line = 1;
  for (let index = 0; index < offset; index += 1) if (text.charCodeAt(index) === 10) line += 1;
  return line;
}

function violation(kind, path, text, offset, value) {
  return { kind, path, line: lineNumber(text, offset), fingerprint: fingerprint(value).slice(0, 12) };
}

function ipv4Number(value) {
  const parts = value.split(".").map(Number);
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return null;
  return (((parts[0] * 256 + parts[1]) * 256 + parts[2]) * 256 + parts[3]) >>> 0;
}

function inCIDR(value, base, bits) {
  const address = ipv4Number(value);
  const network = ipv4Number(base);
  if (address === null || network === null) return false;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return (address & mask) === (network & mask);
}

function publicIPv4(value) {
  const reserved = [
    ["0.0.0.0", 8], ["10.0.0.0", 8], ["100.64.0.0", 10], ["127.0.0.0", 8],
    ["169.254.0.0", 16], ["172.16.0.0", 12], ["192.0.0.0", 24], ["192.0.2.0", 24],
    ["192.88.99.0", 24], ["192.168.0.0", 16], ["198.18.0.0", 15], ["198.51.100.0", 24],
    ["203.0.113.0", 24], ["224.0.0.0", 4], ["240.0.0.0", 4]
  ];
  return ipv4Number(value) !== null && !reserved.some(([base, bits]) => inCIDR(value, base, bits));
}

function publicIPv6(value) {
  if (isIP(value) !== 6) return false;
  const normalized = value.toLowerCase();
  return normalized !== "::" && normalized !== "::1" && !normalized.startsWith("fc") && !normalized.startsWith("fd") && !normalized.startsWith("fe8") && !normalized.startsWith("fe9") && !normalized.startsWith("fea") && !normalized.startsWith("feb") && !normalized.startsWith("ff") && !/^2001:0*db8:/i.test(normalized) && !normalized.startsWith("64:ff9b:1:") && !normalized.startsWith("100:");
}

function allowedDomain(value) {
  const domain = value.toLowerCase().replace(/\.$/, "");
  return allowedDomainRoots.some((root) => domain === root || domain.endsWith(`.${root}`));
}

function domainLooksLikeData(text, offset, value) {
  const before = text.slice(Math.max(0, offset - 16), offset);
  const after = text.slice(offset + value.length, offset + value.length + 8);
  if (/https?:\/\/$/i.test(before)) return true;
  return /["'`]$/.test(before) && /^(?::[0-9]+)?(?:[\/"'`]|$)/.test(after);
}

function ignoredPath(path) {
  return path === "web/package-lock.json" || path.endsWith(".sum") || path.includes("/vendor/");
}

export function scanContent(path, buffer) {
  if (ignoredPath(path) || !buffer.length || buffer.subarray(0, 8192).includes(0)) return [];
  const text = buffer.toString("utf8");
  const violations = [];
  for (const match of text.matchAll(ipv4Pattern)) {
    if (publicIPv4(match[0]) && !allowedPublicIPv4.has(match[0])) violations.push(violation("public-ipv4", path, text, match.index, match[0]));
  }
  for (const match of text.matchAll(ipv6Pattern)) {
    if (/\d/.test(match[0]) && publicIPv6(match[0]) && !allowedPublicIPv6.has(match[0].toLowerCase())) violations.push(violation("public-ipv6", path, text, match.index, match[0]));
  }
  for (const match of text.matchAll(domainPattern)) {
    if (domainLooksLikeData(text, match.index, match[0]) && !allowedDomain(match[0])) violations.push(violation("unapproved-domain", path, text, match.index, match[0]));
  }
  const lines = text.split("\n");
  let offset = 0;
  for (const line of lines) {
    const words = [...line.matchAll(wordPattern)].map((match) => normalizeIdentifier(match[0])).filter(Boolean);
    for (let start = 0; start < words.length; start += 1) {
      for (let length = 1; length <= 4 && start + length <= words.length; length += 1) {
        const candidate = words.slice(start, start + length).join(" ");
        const digest = sha256(candidate);
        if (blockedFingerprints.has(digest)) violations.push(violation("blocked-identifier", path, text, offset, candidate));
      }
    }
    offset += line.length + 1;
  }
  return violations;
}

function worktreeSources() {
  return decodePaths(git(["ls-files", "-z"])).flatMap((path) => {
    try { return [{ path, buffer: readFileSync(resolve(projectDirectory, path)) }]; } catch { return []; }
  });
}

function indexSources() {
  return decodePaths(git(["ls-files", "-z"])).flatMap((path) => {
    try { return [{ path, buffer: git(["show", `:${path}`]) }]; } catch { return []; }
  });
}

function commitSources(commit) {
  const paths = decodePaths(git(["diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", commit]));
  return paths.flatMap((path) => {
    try { return [{ path, buffer: git(["show", `${commit}:${path}`]) }]; } catch { return []; }
  });
}

function prePushSources(input) {
  const commits = new Set();
  for (const line of input.trim().split("\n").filter(Boolean)) {
    const [, localOID, , remoteOID] = line.trim().split(/\s+/);
    if (!localOID || zeroOID.test(localOID)) continue;
    const args = zeroOID.test(remoteOID) ? ["rev-list", localOID, "--not", "--remotes"] : ["rev-list", `${remoteOID}..${localOID}`];
    for (const commit of git(args, { encoding: "utf8" }).trim().split("\n").filter(Boolean)) commits.add(commit);
  }
  return [...commits].flatMap(commitSources);
}

function deduplicate(violations) {
  const seen = new Set();
  return violations.filter((entry) => {
    const key = `${entry.kind}:${entry.path}:${entry.line}:${entry.fingerprint}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function formatViolation(entry) {
  return `${entry.kind} ${entry.path}:${entry.line} fingerprint=${entry.fingerprint}`;
}

function main() {
  const mode = process.argv[2] ?? "--worktree";
  let sources;
  if (mode === "--worktree") sources = worktreeSources();
  else if (mode === "--staged") sources = indexSources();
  else if (mode === "--pre-push") sources = prePushSources(readFileSync(0, "utf8"));
  else throw new Error(`unsupported privacy-check mode: ${mode}`);
  const violations = deduplicate(sources.flatMap(({ path, buffer }) => scanContent(path, buffer)));
  if (!violations.length) return;
  console.error(`privacy-check rejected ${violations.length} finding(s); raw values are intentionally omitted:`);
  for (const entry of violations) console.error(`- ${formatViolation(entry)}`);
  process.exitCode = 1;
}

if (resolve(process.argv[1] ?? "") === fileURLToPath(import.meta.url)) main();
