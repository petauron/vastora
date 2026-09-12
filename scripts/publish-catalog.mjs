#!/usr/bin/env node
// Run only from the protected catalog publication job. GitHub release assets
// are the durable ledger, independent of R2/CDN. No private keys enter assets.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { uploadCatalog } from "./upload-catalog-r2.mjs";

const assetName = "catalog-publication.json";
const origin = "https://downloads.petauron.com/vastora/catalog/";
const hash = bytes => createHash("sha256").update(bytes).digest("hex");
const fileName = name => /^(?:[1-9][0-9]*\.(?:root|targets|snapshot)\.json|targets\/[a-f0-9]{64}\.stable\.json|timestamp\.json|publication-state\.json|manifest-history\.json)$/.test(name);

function hasDurableLedger(release) {
  // GitHub creates a release before uploading its assets. A failed upload can
  // reserve a revision without saving any signed bytes. Only the authenticated
  // asset inventory may prove this; a network/download error is not absence.
  if (!Array.isArray(release.assets)) throw new Error("Cannot verify pending ledger asset inventory");
  const assets = release.assets.filter(asset => asset.name === assetName);
  if (assets.length > 1) throw new Error("Ambiguous publication ledger assets");
  if (assets[0]?.state === "uploaded" && Number.isSafeInteger(assets[0].size) && assets[0].size > 0 && assets[0].size <= 40 * 1024 * 1024) return true;
  if (!release.draft || (assets[0] && assets[0].state !== "starter")) throw new Error("Published ledger asset is missing or invalid");
  return false;
}

export function selectPublication(releases, revision, commit, bootstrap, supersede = false) {
  if (!Number.isSafeInteger(revision) || revision < 1 || !/^[a-f0-9]{40}$/.test(commit)) throw new Error("Invalid publication revision or reviewed commit");
  const entries = releases.filter(release => /^catalog-r[1-9][0-9]*$/.test(release.tag_name)).map(release => ({ ...release, revision: Number(release.tag_name.slice(9)) })).sort((a, b) => b.revision - a.revision);
  if (entries.some(entry => !Number.isSafeInteger(entry.revision))) throw new Error("Invalid ledger revision");
  const latest = entries[0];
  if (latest && revision < latest.revision) throw new Error("Refusing a lower publication revision");
  if (latest?.revision === revision) {
    if (supersede) throw new Error("Supersession requires a new, higher revision");
    if (latest.target_commitish !== commit) throw new Error("Retry must use the original reviewed commit");
    return { resume: latest, previous: entries[1], predecessors: entries.slice(1) };
  }
  if (latest?.draft && !supersede) throw new Error("Resume the pending publication or explicitly supersede it with a higher revision");
  if (supersede && !latest?.draft) throw new Error("Supersession requires a pending publication");
  if (bootstrap !== !latest) throw new Error("Bootstrap is permitted only for the first ledger entry");
  // A timed-out run may have activated before its acknowledgement was lost.
  // Keep every reserved revision and its history; never re-sign expired bytes
  // at the same revision or delete a draft to reset the publication counter.
  return { previous: latest, superseded: supersede ? entries : undefined };
}

export function packagePublication(directory, { revision, commit, runURL, catalogSHA256, supersedesRevision }) {
  if (!lstatSync(directory).isDirectory() || lstatSync(directory).isSymbolicLink()) throw new Error("Invalid staged publication directory");
  const targets = path.join(directory, "targets");
  if (!lstatSync(targets).isDirectory() || lstatSync(targets).isSymbolicLink()) throw new Error("Invalid staged targets directory");
  const files = {};
  const names = readdirSync(directory).flatMap(name => name === "targets" ? readdirSync(path.join(directory, name)).map(child => `targets/${child}`) : [name]);
  for (const name of names) {
    if (!fileName(name)) throw new Error("Unexpected staged publication file");
    const location = path.join(directory, name);
    const stat = lstatSync(location);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 5 * 1024 * 1024) throw new Error("Invalid staged publication file");
    files[name] = readFileSync(location).toString("base64");
  }
  const bundle = { schemaVersion: 1, revision, commit, runURL, catalogSHA256, ...(supersedesRevision === undefined ? {} : { supersedesRevision }), files };
  validatePublication(bundle);
  return bundle;
}

export function validatePublication(bundle) {
  if (bundle.schemaVersion !== 1 || !Number.isSafeInteger(bundle.revision) || bundle.revision <= 0 || !/^[a-f0-9]{40}$/.test(bundle.commit) || !/^[a-f0-9]{64}$/.test(bundle.catalogSHA256)) throw new Error("Invalid publication ledger");
  if (bundle.supersedesRevision !== undefined && (!Number.isSafeInteger(bundle.supersedesRevision) || bundle.supersedesRevision <= 0 || bundle.supersedesRevision >= bundle.revision)) throw new Error("Invalid supersession ledger");
  if (!bundle.files || Object.keys(bundle.files).length > 1000) throw new Error("Invalid ledger files");
  let bytes = 0;
  for (const [name, encoded] of Object.entries(bundle.files)) {
    if (!fileName(name) || typeof encoded !== "string") throw new Error("Unexpected ledger file");
    const raw = Buffer.from(encoded, "base64");
    bytes += raw.length;
    if (raw.toString("base64") !== encoded || raw.length === 0 || raw.length > 5 * 1024 * 1024 || bytes > 30 * 1024 * 1024) throw new Error("Invalid ledger encoding or size");
  }
  const state = JSON.parse(Buffer.from(bundle.files["publication-state.json"] ?? "", "base64"));
  const history = JSON.parse(Buffer.from(bundle.files["manifest-history.json"] ?? "", "base64"));
  if (state.revision !== bundle.revision || state.channel !== "stable" || !/^[a-f0-9]{64}$/.test(state.sha256) || Object.keys(history).length === 0) throw new Error("Ledger acceptance does not match publication");
  const target = Buffer.from(bundle.files[`targets/${state.sha256}.stable.json`] ?? "", "base64");
  if (hash(target) !== state.sha256 || !bundle.files["timestamp.json"] || !bundle.files[`${state.revision}.targets.json`] || !bundle.files[`${state.revision}.snapshot.json`]) throw new Error("Incomplete publication ledger");
  return state;
}

export function unpackPublication(bundle, directory) {
  validatePublication(bundle);
  mkdirSync(directory, { mode: 0o700 });
  mkdirSync(path.join(directory, "targets"), { mode: 0o700 });
  for (const [name, encoded] of Object.entries(bundle.files)) writeFileSync(path.join(directory, name), Buffer.from(encoded, "base64"), { mode: 0o600, flag: "wx" });
}

export function publishCatalog(options, run = execFileSync, upload = uploadCatalog) {
  const { revision, commit, repository, work, rootDirectory, catalog, binDirectory, bucket, endpoint, bootstrap = false, supersede = false, runURL } = options;
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository ?? "") || !work || !rootDirectory || !binDirectory || !catalog) throw new Error("Invalid publication configuration");
  mkdirSync(work, { mode: 0o700 }); // caller supplies a new, isolated workspace
  const execute = (command, args) => run(command, args, { stdio: ["ignore", "pipe", "pipe"], maxBuffer: 40 * 1024 * 1024 });
  const gh = (...args) => execute("gh", [...args, "--repo", repository]);
  const pages = JSON.parse(execute("gh", ["api", "--paginate", "--slurp", `repos/${repository}/releases?per_page=100`]));
  const plan = selectPublication(pages.flat(), revision, commit, bootstrap, supersede);
  const tag = `catalog-r${revision}`;
  const catalogSHA256 = hash(readFileSync(catalog));
  const ledgers = new Map();
  const readLedger = release => {
    if (ledgers.has(release.revision)) return ledgers.get(release.revision);
    const dir = path.join(work, `ledger-${release.revision}`);
    mkdirSync(dir, { mode: 0o700 });
    gh("release", "download", release.tag_name, "--pattern", assetName, "--dir", dir);
    const raw = readFileSync(path.join(dir, assetName));
    if (raw.length > 40 * 1024 * 1024) throw new Error("Publication ledger too large");
    const bundle = JSON.parse(raw);
    validatePublication(bundle);
    if (bundle.commit !== release.target_commitish || bundle.revision !== release.revision) throw new Error("Release and ledger disagree");
    ledgers.set(release.revision, bundle);
    return bundle;
  };
  const staged = path.join(work, "staged");
  let bundle;
  let previous;
  if (plan.resume) {
    bundle = readLedger(plan.resume);
    if (bundle.catalogSHA256 !== catalogSHA256) throw new Error("Retry catalog differs from the approved publication");
    if (bundle.supersedesRevision !== undefined) {
      if (bundle.supersedesRevision !== plan.previous?.revision) throw new Error("Supersession predecessor differs from protected ledger");
      plan.superseded = plan.predecessors;
    }
  }
  const storagePredecessors = plan.superseded?.filter(hasDurableLedger);
  const predecessor = storagePredecessors ? storagePredecessors[0] : plan.previous;
  if (predecessor) previous = readLedger(predecessor);
  if (plan.resume) {
    unpackPublication(bundle, staged);
  } else {
    const args = ["--catalog", catalog, "--revision", String(revision), "--output", staged, "--valid-for", "168h"];
    if (previous) {
      const prior = path.join(work, "previous");
      unpackPublication(previous, prior);
      args.push("--previous", path.join(prior, "publication-state.json"), "--history", path.join(prior, "manifest-history.json"));
    } else args.push("--bootstrap");
    const roots = readdirSync(rootDirectory).filter(name => /^[1-9][0-9]*\.root\.json$/.test(name)).sort((a, b) => Number(a.split(".")[0]) - Number(b.split(".")[0]));
    if (roots.length === 0 || roots.some((name, i) => name !== `${i + 1}.root.json`)) throw new Error("An independently reviewed, consecutive root chain is required");
    for (const name of roots) {
      const info = lstatSync(path.join(rootDirectory, name));
      if (!info.isFile() || info.isSymbolicLink() || info.size > 1024 * 1024) throw new Error("Invalid reviewed root file");
    }
    args.push("--root", path.join(rootDirectory, roots.at(-1)));
    for (const role of ["targets", "snapshot", "timestamp"]) {
      if (!options.keyFiles?.[role]) throw new Error("Protected publication signer is missing");
      args.push(`--${role}-keys`, options.keyFiles[role]);
    }
    execute(path.join(binDirectory, "catalog-publish"), args);
    for (const root of roots) copyFileSync(path.join(rootDirectory, root), path.join(staged, root));
    bundle = packagePublication(staged, { revision, commit, runURL, catalogSHA256, supersedesRevision: plan.superseded ? plan.previous.revision : undefined });
  }
  const state = validatePublication(bundle);
  const verifyArgs = ["--root", path.join(rootDirectory, "1.root.json"), "--revision", String(revision), "--sha256", state.sha256];
  execute(path.join(binDirectory, "catalog-verify"), ["--directory", staged, ...verifyArgs]);
  if (plan.resume && !plan.resume.draft) {
    execute(path.join(binDirectory, "catalog-verify"), ["--origin", origin, ...verifyArgs]);
    return; // already complete: do not write or sign again
  }
  if (!plan.resume) {
    const asset = path.join(work, assetName);
    writeFileSync(asset, JSON.stringify(bundle), { mode: 0o600, flag: "wx" });
    gh("release", "create", tag, asset, "--draft", "--prerelease", "--latest=false", "--target", commit, "--title", `Official catalog r${revision}`, "--notes", `Application catalog only; no Center/Agent release.\nReviewed commit: ${commit}\nApproval/run: ${runURL}\nTarget SHA256: ${state.sha256}${plan.superseded ? `\nSupersedes pending revision: ${plan.previous.revision}; prior ledger retained.` : ""}`);
    // Do not publish R2 bytes unless durable storage of these exact signatures
    // has been confirmed. Failed verification leaves a draft for investigation.
    const persisted = readLedger({ tag_name: tag, revision, target_commitish: commit });
    if (JSON.stringify(persisted) !== JSON.stringify(bundle)) throw new Error("Durable publication ledger did not verify");
  }
  // Observe and compare the previous timestamp via authenticated storage, not
  // the CDN. An interrupted activation can only resume the exact saved bytes.
  const output = path.join(work, "r2-timestamp.json");
  const awsArgs = ["--cli-connect-timeout", "10", "--cli-read-timeout", "60", "s3api", "get-object", "--bucket", bucket, "--endpoint-url", endpoint, "--key", "vastora/catalog/timestamp.json", output, "--no-cli-pager"];
  let previousETag;
  let activated = false;
  if (previous || plan.resume || plan.superseded) {
    let response;
    try { response = JSON.parse(execute("aws", awsArgs)); }
    catch (error) {
      // An absent first timestamp is expected only for a bootstrap retry.
      // NoSuchKey is explicit; permission/transport/unknown errors fail closed.
      const unpublishedBootstrap = plan.superseded?.every(release => release.draft);
      if ((previous && !unpublishedBootstrap) || !/\(NoSuchKey\)/.test(String(error.stderr ?? ""))) throw error;
    }
    if (response) {
      const active = readFileSync(output);
      activated = active.equals(Buffer.from(bundle.files["timestamp.json"], "base64"));
      const matchesLedger = candidate => active.equals(Buffer.from(candidate.files["timestamp.json"], "base64"));
      // Supersession may recover an expired draft whether it activated or not.
      // Its observed predecessor must still be accounted for by a durable,
      // authenticated ledger; arbitrary storage bytes remain a hard failure.
      const accountedFor = (previous && matchesLedger(previous)) || storagePredecessors?.some(release => matchesLedger(readLedger(release)));
      if (!activated && !accountedFor) throw new Error("R2 timestamp differs from protected ledger");
      previousETag = response.ETag;
    }
  }
  if (!activated) upload({ directory: staged, bucket, endpoint, bootstrap: !previousETag, previousETag }, run);
  execute(path.join(binDirectory, "catalog-verify"), ["--origin", origin, ...verifyArgs]);
  gh("release", "edit", tag, "--draft=false", "--prerelease", "--latest=false");
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  try {
    publishCatalog({ revision: Number(process.env.CATALOG_REVISION), commit: process.env.GITHUB_SHA, repository: process.env.GITHUB_REPOSITORY, work: process.env.CATALOG_WORK, rootDirectory: "catalog/trust", catalog: "catalog/catalog.json", binDirectory: process.env.CATALOG_BIN, bucket: process.env.R2_BUCKET_NAME, endpoint: process.env.R2_ENDPOINT, bootstrap: process.env.CATALOG_BOOTSTRAP === "true", supersede: process.env.CATALOG_SUPERSEDE === "true", runURL: `https://github.com/${process.env.GITHUB_REPOSITORY}/actions/runs/${process.env.GITHUB_RUN_ID}`, keyFiles: Object.fromEntries(["targets", "snapshot", "timestamp"].map(role => [role, process.env[`CATALOG_${role.toUpperCase()}_KEY_FILE`]])) });
  } catch {
    console.error("Catalog publication was not confirmed. Preserve the draft and retry the original run; an expired or incomplete draft requires approved supersession with a higher revision. Never overwrite unconditionally.");
    process.exitCode = 1;
  }
}
