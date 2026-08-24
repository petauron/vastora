import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = (await readFile(
  new URL("../deploy/installer-worker/worker.js", import.meta.url),
  "utf8",
)).replace('import { connect } from "cloudflare:sockets";', "const connect = globalThis.__vastoraConnect;");
let connectImplementation = () => { throw new Error("unexpected TCP connection"); };
globalThis.__vastoraConnect = (...arguments_) => connectImplementation(...arguments_);
const workerModule = await import(
  `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`
);
const worker = workerModule.default;
const { OAuthSession } = workerModule;
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

async function oauthCommitment(secret) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(secret));
  return Buffer.from(digest).toString("base64url");
}

function oauthEnvironment() {
  const sessions = new Map();
  return {
    OAUTH_SESSIONS: {
      idFromName(name) {
        return name;
      },
      get(id) {
        if (!sessions.has(id)) {
          const values = new Map();
          const storage = {
            alarm: null,
            async get(key) { return values.get(key); },
            async put(key, value) { values.set(key, value); },
            async setAlarm(value) { this.alarm = value; },
            async deleteAll() { values.clear(); },
          };
          sessions.set(id, { object: new OAuthSession({ storage }), storage });
        }
        return {
          fetch: (input, init) => sessions.get(id).object.fetch(
            input instanceof Request ? input : new Request(input, init),
          ),
        };
      },
    },
    sessions,
  };
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

{
  const publicAddressRequest = request("/network/public-address");
  publicAddressRequest.headers.set("CF-Connecting-IP", "192.0.2.42");
  const response = await worker.fetch(publicAddressRequest, {}, executionContext().context);
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { address: "192.0.2.42" });
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.equal((await worker.fetch(request("/network/public-address"), {}, executionContext().context)).status, 503);
}

{
  const connected = [];
  connectImplementation = ({ hostname, port }) => {
    connected.push({ hostname, port });
    let controller;
    const readable = new ReadableStream({ start(value) { controller = value; } });
    const writable = new WritableStream({
      write(chunk) {
        const requestLine = new TextDecoder().decode(chunk);
        const challenge = requestLine.match(/^VASTORA-PROBE\/1 ([A-Za-z0-9_-]{43})\n$/)?.[1];
        controller.enqueue(new TextEncoder().encode(`VASTORA-OK/1 ${challenge}\n`));
        controller.close();
      },
    });
    return { opened: Promise.resolve({}), readable, writable, close: async () => {} };
  };
  const challenge = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ";
  const verifyRequest = new Request("https://vastora.petauron.com/network/verify-public-entry", {
    method: "POST",
    headers: { "CF-Connecting-IP": "192.0.2.42", "Content-Type": "application/json" },
    body: JSON.stringify({ ports: [80, 443], challenge }),
  });
  const response = await worker.fetch(verifyRequest, {}, executionContext().context);
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { status: "ready", address: "192.0.2.42", ports: [{ port: 80, ready: true }, { port: 443, ready: true }] });
  assert.deepEqual(connected, [{ hostname: "192.0.2.42", port: 80 }, { hostname: "192.0.2.42", port: 443 }]);
}

{
  connectImplementation = ({ port }) => {
    if (port === 443) throw new Error("unreachable");
    let controller;
    const readable = new ReadableStream({ start(value) { controller = value; } });
    const writable = new WritableStream({
      write(chunk) {
        const challenge = new TextDecoder().decode(chunk).match(/^VASTORA-PROBE\/1 ([A-Za-z0-9_-]{43})\n$/)?.[1];
        controller.enqueue(new TextEncoder().encode(`VASTORA-OK/1 ${challenge}\n`));
        controller.close();
      },
    });
    return { opened: Promise.resolve({}), readable, writable, close: async () => {} };
  };
  const failedRequest = new Request("https://vastora.petauron.com/network/verify-public-entry", {
    method: "POST",
    headers: { "CF-Connecting-IP": "192.0.2.42", "Content-Type": "application/json" },
    body: JSON.stringify({ ports: [80, 443], challenge: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ" }),
  });
  const failed = await worker.fetch(failedRequest, {}, executionContext().context);
  assert.equal(failed.status, 409);
  assert.deepEqual((await failed.json()).ports, [{ port: 80, ready: true }, { port: 443, ready: false }]);

  const invalidRequest = new Request("https://vastora.petauron.com/network/verify-public-entry", {
    method: "POST",
    headers: { "CF-Connecting-IP": "192.0.2.42", "Content-Type": "application/json" },
    body: JSON.stringify({ ports: [22, 443], challenge: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ" }),
  });
  assert.equal((await worker.fetch(invalidRequest, {}, executionContext().context)).status, 400);
}

{
  const env = oauthEnvironment();
  const secret = "relay-secret-with-at-least-thirty-two-bytes";
  const stateID = "oauth-session-id-1234567890123456";
  const state = `v1.${stateID}.${await oauthCommitment(secret)}`;
  const callback = await worker.fetch(
    request(`/oauth/cloudflare/callback?code=one-time-code&state=${encodeURIComponent(state)}`),
    env,
    executionContext().context,
  );
  assert.equal(callback.status, 200);
  assert.match(await callback.text(), /Cloudflare 已连接/);

  const pollRequest = request(`/oauth/cloudflare/sessions/${encodeURIComponent(state)}`);
  pollRequest.headers.set("Authorization", `Bearer ${secret}`);
  const poll = await worker.fetch(pollRequest, env, executionContext().context);
  assert.equal(poll.status, 200);
  assert.deepEqual(await poll.json(), { status: "authorized", code: "one-time-code" });

  const replay = await worker.fetch(pollRequest, env, executionContext().context);
  assert.equal(replay.status, 202);
  assert.deepEqual(await replay.json(), { status: "pending" });
}

{
  const env = oauthEnvironment();
  const secret = "another-relay-secret-with-thirty-two-bytes";
  const stateID = "oauth-session-id-6543210987654321";
  const state = `v1.${stateID}.${await oauthCommitment(secret)}`;
  const callback = await worker.fetch(
    request(`/oauth/cloudflare/callback?error=access_denied&error_description=${encodeURIComponent("Denied <script>alert(1)</script>")}&state=${encodeURIComponent(state)}`),
    env,
    executionContext().context,
  );
  assert.equal(callback.status, 400);
  const body = await callback.text();
  assert.doesNotMatch(body, /<script>alert/);
  assert.match(body, /&lt;script&gt;/);

  const pollRequest = request(`/oauth/cloudflare/sessions/${encodeURIComponent(state)}`);
  pollRequest.headers.set("Authorization", "Bearer wrong-secret");
  assert.equal((await worker.fetch(pollRequest, env, executionContext().context)).status, 401);
}

{
  const response = await worker.fetch(
    request("/oauth/cloudflare/callback?code=test&state=invalid"),
    oauthEnvironment(),
    executionContext().context,
  );
  assert.equal(response.status, 400);
  assert.equal(response.headers.get("cache-control"), "no-store");
}

assert.deepEqual(workerLogs.map(([level]) => level), ["warn", "error"]);
console.warn = originalWarn;
console.error = originalError;
console.log("installer worker tests: OK");
