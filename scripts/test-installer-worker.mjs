import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(
  new URL("../deploy/installer-worker/worker.js", import.meta.url),
  "utf8",
);
const worker = (
  await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`)
).default;
const originalWarn = console.warn;
const originalError = console.error;
const workerLogs = [];
console.warn = (...arguments_) => workerLogs.push(["warn", ...arguments_]);
console.error = (...arguments_) => workerLogs.push(["error", ...arguments_]);

function cacheStorage() {
  const entries = new Map();
  return {
    entries,
    cache: {
      async match(request) {
        return entries.get(request.url)?.clone();
      },
      async put(request, response) {
        entries.set(request.url, response.clone());
      },
    },
  };
}

function executionContext() {
  const pending = [];
  return {
    context: {
      waitUntil(promise) {
        pending.push(promise);
      },
    },
    flush: () => Promise.all(pending),
  };
}

function request(path = "/install.sh", method = "GET") {
  return new Request(`https://vastora.petauron.com${path}`, { method });
}

{
  const storage = cacheStorage();
  globalThis.caches = { default: storage.cache };
  const fetched = [];
  globalThis.fetch = async (url) => {
    fetched.push(String(url));
    if (new URL(url).hostname === "raw.githubusercontent.com") {
      return new Response("0.1.0-alpha.4\n");
    }
    return new Response(null, { status: 302 });
  };

  const execution = executionContext();
  const response = await worker.fetch(request(), {}, execution.context);
  await execution.flush();
  assert.equal(response.status, 302);
  assert.equal(
    response.headers.get("location"),
    "https://github.com/petauron/vastora/releases/download/v0.1.0-alpha.4/install.sh",
  );
  assert.equal(fetched.length, 4);

  const cachedResponse = await worker.fetch(request("/vastora-center-install.tar.gz"), {}, executionContext().context);
  assert.equal(cachedResponse.status, 302);
  assert.equal(fetched.length, 4);

  storage.entries.delete("https://vastora-installer.internal/current-release");
  globalThis.fetch = async () => new Response(null, { status: 503 });
  const fallback = await worker.fetch(request(), {}, executionContext().context);
  assert.equal(fallback.status, 302);
}

{
  const storage = cacheStorage();
  globalThis.caches = { default: storage.cache };
  globalThis.fetch = async () => new Response(null, { status: 503 });
  const response = await worker.fetch(request(), {}, executionContext().context);
  assert.equal(response.status, 503);
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.equal(response.headers.get("cloudflare-cdn-cache-control"), "no-store");
}

{
  const response = await worker.fetch(request("/install.sh", "POST"), {}, executionContext().context);
  assert.equal(response.status, 405);
}

assert.deepEqual(workerLogs.map(([level]) => level), ["warn", "error"]);
console.warn = originalWarn;
console.error = originalError;
console.log("installer worker tests: OK");
