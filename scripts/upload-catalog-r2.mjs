#!/usr/bin/env node
// Transport only: signing/verification and publication approval are mandatory
// upstream steps. Never reads key material or uploads publication-state.json.
import { execFileSync } from "node:child_process";
import { readdirSync, lstatSync, readFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

export function uploadCatalog({ directory, bucket, endpoint, previousETag, bootstrap = false }, run = execFileSync) {
  if (!directory || !/^[a-z0-9][a-z0-9.-]+$/.test(bucket ?? "") || !/^https:\/\/[a-z0-9]+\.r2\.cloudflarestorage\.com$/.test(endpoint ?? "")) throw new Error("Invalid catalog upload destination");
  if (bootstrap === Boolean(previousETag) || (previousETag && !/^"[a-zA-Z0-9-]+"$/.test(previousETag))) throw new Error("Supply bootstrap or an exact previous ETag");
  if (!lstatSync(directory).isDirectory() || lstatSync(directory).isSymbolicLink()) throw new Error("Invalid staging directory");
  const files = [];
  for (const entry of readdirSync(directory)) {
    if (entry === "publication-state.json" || entry === "manifest-history.json") continue;
    const local = path.join(directory, entry);
    if (entry === "targets") {
      if (!lstatSync(local).isDirectory() || lstatSync(local).isSymbolicLink()) throw new Error("Invalid target directory");
      for (const target of readdirSync(local)) files.push(`targets/${target}`);
    } else files.push(entry);
  }
  for (const name of files) {
    if (!/^(?:[1-9][0-9]*\.(?:root|targets|snapshot)\.json|targets\/[a-f0-9]{64}\.stable\.json|timestamp\.json)$/.test(name)) throw new Error("Unexpected publication file");
    const stat = lstatSync(path.join(directory, name));
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size <= 0 || stat.size > 5 * 1024 * 1024) throw new Error("Invalid publication file");
  }
  if (!files.includes("timestamp.json") || !files.some(name => name.startsWith("targets/")) || !files.some(name => name.endsWith(".root.json")) || !files.some(name => name.endsWith(".snapshot.json")) || !files.some(name => name.endsWith(".targets.json"))) throw new Error("Incomplete publication");
  const temporary = mkdtempSync(path.join(tmpdir(), "vastora-catalog-upload-"));
  const aws = (...args) => run("aws", ["--cli-connect-timeout", "10", "--cli-read-timeout", "60", "s3api", ...args, "--bucket", bucket, "--endpoint-url", endpoint, "--no-cli-pager"], { stdio: ["ignore", "pipe", "pipe"] });
  try {
    for (const name of files.filter(name => name !== "timestamp.json").sort()) {
      const local = path.join(directory, name);
      const key = `vastora/catalog/${name}`;
      try {
        aws("put-object", "--key", key, "--body", local, "--if-none-match", "*", "--content-type", "application/json", "--cache-control", "public,max-age=31536000,immutable");
      } catch {
        // A failed conditional write is harmless only if the existing bytes
        // exactly match. Permissions/network failures still fail this read.
        const existing = path.join(temporary, "existing.json");
        aws("get-object", "--key", key, existing);
        if (!readFileSync(existing).equals(readFileSync(local))) throw new Error("Immutable catalog object conflicts with published bytes");
      }
      const verified = path.join(temporary, "verified.json");
      aws("get-object", "--key", key, verified);
      if (!readFileSync(verified).equals(readFileSync(local))) throw new Error("Uploaded catalog bytes did not verify");
    }
    // No retry without the condition: another publication must not be replaced.
    aws("put-object", "--key", "vastora/catalog/timestamp.json", "--body", path.join(directory, "timestamp.json"), bootstrap ? "--if-none-match" : "--if-match", bootstrap ? "*" : previousETag, "--content-type", "application/json", "--cache-control", "no-cache,max-age=0,must-revalidate");
    const active = path.join(temporary, "active.json");
    aws("get-object", "--key", "vastora/catalog/timestamp.json", active);
    if (!readFileSync(active).equals(readFileSync(path.join(directory, "timestamp.json")))) throw new Error("Catalog activation was not confirmed");
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  try {
    const args = process.argv.slice(2);
    const options = {};
    while (args.length) {
      const key = args.shift();
      if (key === "--bootstrap") options.bootstrap = true;
      else if (["--directory", "--bucket", "--endpoint", "--previous-etag"].includes(key)) options[key === "--previous-etag" ? "previousETag" : key.slice(2)] = args.shift();
      else throw new Error("Unknown argument");
    }
    uploadCatalog(options);
  } catch {
    // SDK errors may contain endpoint/auth context. Keep CI failure output safe.
    console.error("Catalog upload failed; publication was not confirmed.");
    process.exitCode = 1;
  }
}
