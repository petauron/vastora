import { connect } from "cloudflare:sockets";

const repository = "petauron/vastora";
const releaseAssets = new Map([
  ["install.sh", "text/x-shellscript; charset=utf-8"],
  ["vastora-center-install.tar.gz", "application/gzip"],
  ["vastora-center-install.tar.gz.sha256", "text/plain; charset=utf-8"],
]);
const releaseVersionPattern = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/;
const immutableCacheControl = "public, max-age=31536000, immutable";
const currentCacheControl = "public, max-age=60";
const currentManifestKey = "vastora/current.json";
const oauthCallbackPath = "/oauth/cloudflare/callback";
const oauthSessionPrefix = "/oauth/cloudflare/sessions/";
const publicAddressPath = "/network/public-address";
const verifyPublicEntryPath = "/network/verify-public-entry";
const oauthSessionLifetimeMs = 10 * 60 * 1000;
const oauthStatePattern = /^v1\.([A-Za-z0-9_-]{24,64})\.([A-Za-z0-9_-]{43})$/;

function validManifest(manifest) {
  if (!manifest || manifest.schema !== 1 || !releaseVersionPattern.test(manifest.version || "")) {
    return false;
  }
  if (!manifest.assets || Object.keys(manifest.assets).length !== releaseAssets.size) return false;
  const prefix = `vastora/releases/v${manifest.version}/`;
  for (const asset of releaseAssets.keys()) {
    const descriptor = manifest.assets[asset];
    if (!descriptor || descriptor.key !== `${prefix}${asset}` || !/^[0-9a-f]{64}$/.test(descriptor.sha256 || "")) {
      return false;
    }
  }
  return true;
}

function manifestCacheKey(key) {
  return new Request(`https://vastora-installer.internal/${key}`);
}

async function releaseManifest(env, ctx, key, cacheControl) {
  if (!env.INSTALLER_ASSETS) throw new Error("Installer R2 binding is unavailable");
  const cache = caches.default;
  const cacheKey = manifestCacheKey(key);
  const cached = await cache.match(cacheKey);
  if (cached) return cached.json();
  const object = await env.INSTALLER_ASSETS.get(key);
  if (!object) return null;
  const manifest = await object.json();
  if (!validManifest(manifest)) throw new Error("Installer release manifest is invalid");
  ctx.waitUntil(cache.put(cacheKey, Response.json(manifest, {
    headers: { "Cache-Control": cacheControl },
  })));
  return manifest;
}

async function selectedRelease(env, ctx) {
  const manifest = await releaseManifest(env, ctx, currentManifestKey, currentCacheControl);
  if (!manifest) throw new Error("Installer release manifest is unavailable");
  return manifest;
}

async function versionedRelease(env, ctx, version) {
  const manifest = await releaseManifest(
    env,
    ctx,
    `vastora/releases/v${version}/activated.json`,
    immutableCacheControl,
  );
  if (manifest && manifest.version !== version) throw new Error("Installer release manifest version is mismatched");
  return manifest;
}

function versionedAsset(pathname) {
  const match = /^\/releases\/v([^/]+)\/([^/]+)$/.exec(pathname);
  if (!match || !releaseVersionPattern.test(match[1]) || !releaseAssets.has(match[2])) return null;
  return { version: match[1], asset: match[2] };
}

async function installerAssetResponse(request, env, manifest, asset, cacheControl) {
  const descriptor = manifest.assets[asset];
  const object = request.method === "HEAD"
    ? await env.INSTALLER_ASSETS.head(descriptor.key)
    : await env.INSTALLER_ASSETS.get(descriptor.key);
  if (!object || object.customMetadata?.sha256 !== descriptor.sha256) {
    throw new Error(`Installer release object is unavailable or mismatched: ${asset}`);
  }
  const headers = new Headers(responseHeaders());
  object.writeHttpMetadata(headers);
  headers.set("Cache-Control", cacheControl);
  headers.set("Content-Length", String(object.size));
  headers.set("Content-Type", releaseAssets.get(asset));
  headers.set("ETag", object.httpEtag);
  headers.set("X-Vastora-SHA256", descriptor.sha256);
  headers.set("X-Vastora-Version", manifest.version);
  return new Response(request.method === "HEAD" ? null : object.body, { headers });
}

function responseHeaders() {
  return {
    "Cache-Control": "public, max-age=60",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
  };
}

function oauthHeaders(contentType = "application/json; charset=utf-8") {
  return {
    "Cache-Control": "no-store",
    "Cloudflare-CDN-Cache-Control": "no-store",
    "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
    "Content-Type": contentType,
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
  };
}

function handlePublicAddress(request) {
  const address = (request.headers.get("CF-Connecting-IP") || "").trim();
  if (!address || address.length > 45 || !/^[0-9a-f:.]+$/i.test(address)) {
    return new Response("Public address unavailable\n", {
      status: 503,
      headers: oauthHeaders("text/plain; charset=utf-8"),
    });
  }
  return Response.json({ address }, { headers: oauthHeaders() });
}

async function handlePublicEntryVerification(request) {
  const sourceAddress = (request.headers.get("CF-Connecting-IP") || "").trim();
  if (!sourceAddress || sourceAddress.length > 45 || !/^[0-9a-f:.]+$/i.test(sourceAddress)) {
    return Response.json({ status: "unavailable", error: "The caller public address is unavailable." }, { status: 503, headers: oauthHeaders() });
  }
  const contentLength = Number(request.headers.get("Content-Length") || "0");
  if (contentLength > 2048) {
    return Response.json({ status: "invalid", error: "The verification request is too large." }, { status: 413, headers: oauthHeaders() });
  }
  let input;
  try {
    input = await request.json();
  } catch {
    return Response.json({ status: "invalid", error: "The verification request is invalid." }, { status: 400, headers: oauthHeaders() });
  }
  const ports = input?.ports;
  const challenge = String(input?.challenge || "");
  if (!Array.isArray(ports) || ports.length !== 2 || ports[0] !== 80 || ports[1] !== 443 || !/^[A-Za-z0-9_-]{43}$/.test(challenge)) {
    return Response.json({ status: "invalid", error: "The public entry challenge is invalid." }, { status: 400, headers: oauthHeaders() });
  }
  const results = await Promise.all(ports.map(async (port) => {
    try {
      await probePublicPort(sourceAddress, port, challenge);
      return { port, ready: true };
    } catch {
      return { port, ready: false };
    }
  }));
  if (results.some((result) => !result.ready)) {
    return Response.json({ status: "unreachable", address: sourceAddress, ports: results, error: "The public address did not reach every required TCP port." }, { status: 409, headers: oauthHeaders() });
  }
  return Response.json({ status: "ready", address: sourceAddress, ports: results }, { headers: oauthHeaders() });
}

async function probePublicPort(address, port, challenge) {
  const socket = connect({ hostname: address, port }, { allowHalfOpen: true, secureTransport: "off" });
  const timeout = (promise) => {
    let timer;
    return Promise.race([
      promise,
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("probe timed out")), 8000); }),
    ]).finally(() => clearTimeout(timer));
  };
  try {
    await timeout(socket.opened);
    const writer = socket.writable.getWriter();
    try {
      await timeout(writer.write(new TextEncoder().encode(`VASTORA-PROBE/1 ${challenge}\n`)));
    } finally {
      writer.releaseLock();
    }
    const reader = socket.readable.getReader();
    let response = "";
    try {
      while (response.length <= 256 && !response.includes("\n")) {
        const result = await timeout(reader.read());
        if (result.done) break;
        response += new TextDecoder().decode(result.value, { stream: true });
      }
    } finally {
      reader.releaseLock();
    }
    if (response !== `VASTORA-OK/1 ${challenge}\n`) throw new Error("challenge mismatch");
  } finally {
    await socket.close().catch(() => {});
  }
}

function parseOAuthState(value) {
  const match = oauthStatePattern.exec(value || "");
  return match ? { id: match[1], commitment: match[2] } : null;
}

function oauthSession(env, id) {
  if (!env.OAUTH_SESSIONS) return null;
  return env.OAUTH_SESSIONS.get(env.OAUTH_SESSIONS.idFromName(id));
}

function oauthResultPage(ok, message) {
  const title = ok ? "Cloudflare 已连接" : "Cloudflare 授权失败";
  const detail = escapeHTML(ok ? "可以关闭此页面并返回 Vastora，安装向导会自动继续。" : message);
  return new Response(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>${title}</title><style>body{font:16px system-ui,sans-serif;display:grid;min-height:100vh;margin:0;place-items:center;background:#f7f7f8;color:#18181b}.card{max-width:32rem;margin:2rem;padding:2rem;border:1px solid #ddd;border-radius:1rem;background:white}h1{font-size:1.25rem;margin:0 0 .75rem}p{line-height:1.6;margin:0;color:#52525b}</style><main class="card"><h1>${title}</h1><p>${detail}</p></main></html>`, {
    status: ok ? 200 : 400,
    headers: oauthHeaders("text/html; charset=utf-8"),
  });
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]);
}

async function handleOAuthCallback(url, env) {
  const parsed = parseOAuthState(url.searchParams.get("state"));
  if (!parsed) return oauthResultPage(false, "授权状态无效或已经过期。");
  const session = oauthSession(env, parsed.id);
  if (!session) return new Response("OAuth relay is unavailable\n", { status: 503, headers: oauthHeaders("text/plain; charset=utf-8") });
  const code = url.searchParams.get("code") || "";
  const error = url.searchParams.get("error") || "";
  const errorDescription = url.searchParams.get("error_description") || "";
  if ((!code && !error) || code.length > 4096 || error.length > 128 || errorDescription.length > 1024) {
    return oauthResultPage(false, "Cloudflare 返回了无效的授权结果。");
  }
  const stored = await session.fetch("https://oauth-session.internal/callback", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ commitment: parsed.commitment, code, error, errorDescription }),
  });
  if (!stored.ok) return oauthResultPage(false, "授权会话已经使用或已经过期。");
  return oauthResultPage(!error, errorDescription || error || "Cloudflare 拒绝了授权。");
}

async function handleOAuthPoll(request, url, env) {
  const encodedState = url.pathname.slice(oauthSessionPrefix.length);
  let state;
  try {
    state = decodeURIComponent(encodedState);
  } catch {
    return new Response("Invalid OAuth state\n", { status: 400, headers: oauthHeaders("text/plain; charset=utf-8") });
  }
  const parsed = parseOAuthState(state);
  const authorization = request.headers.get("Authorization") || "";
  if (!parsed || !authorization.startsWith("Bearer ") || authorization.length > 256) {
    return new Response("Unauthorized\n", { status: 401, headers: oauthHeaders("text/plain; charset=utf-8") });
  }
  const session = oauthSession(env, parsed.id);
  if (!session) return new Response("OAuth relay is unavailable\n", { status: 503, headers: oauthHeaders("text/plain; charset=utf-8") });
  return session.fetch("https://oauth-session.internal/poll", {
    headers: {
      Authorization: authorization,
      "X-Vastora-OAuth-Commitment": parsed.commitment,
    },
  });
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname === publicAddressPath) {
      if (request.method !== "GET") return new Response("Method not allowed\n", { status: 405, headers: { ...oauthHeaders("text/plain; charset=utf-8"), Allow: "GET" } });
      return handlePublicAddress(request);
    }
    if (url.pathname === verifyPublicEntryPath) {
      if (request.method !== "POST") return new Response("Method not allowed\n", { status: 405, headers: { ...oauthHeaders("text/plain; charset=utf-8"), Allow: "POST" } });
      return handlePublicEntryVerification(request);
    }
    if (url.pathname === oauthCallbackPath) {
      if (request.method !== "GET") return new Response("Method not allowed\n", { status: 405, headers: { ...oauthHeaders("text/plain; charset=utf-8"), Allow: "GET" } });
      return handleOAuthCallback(url, env);
    }
    if (url.pathname.startsWith(oauthSessionPrefix)) {
      if (request.method !== "GET") return new Response("Method not allowed\n", { status: 405, headers: { ...oauthHeaders("text/plain; charset=utf-8"), Allow: "GET" } });
      return handleOAuthPoll(request, url, env);
    }
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("Method not allowed\n", {
        status: 405,
        headers: { ...responseHeaders(), Allow: "GET, HEAD" },
      });
    }

    if (url.pathname === "/") {
      return Response.redirect(`https://github.com/${repository}`, 302);
    }
    const immutableAsset = versionedAsset(url.pathname);
    const currentAsset = url.pathname.slice(1);
    if (!immutableAsset && !releaseAssets.has(currentAsset)) {
      return new Response("Not found\n", { status: 404, headers: responseHeaders() });
    }

    try {
      if (immutableAsset) {
        const manifest = await versionedRelease(env, ctx, immutableAsset.version);
        if (!manifest) return new Response("Not found\n", { status: 404, headers: responseHeaders() });
        return await installerAssetResponse(request, env, manifest, immutableAsset.asset, immutableCacheControl);
      }
      const manifest = await selectedRelease(env, ctx);
      return await installerAssetResponse(request, env, manifest, currentAsset, currentCacheControl);
    } catch (error) {
      console.error("Unable to serve installer release", error);
      return new Response("No complete Vastora release is currently available.\n", {
        status: 503,
        headers: {
          ...responseHeaders(),
          "Cache-Control": "no-store",
          "Cloudflare-CDN-Cache-Control": "no-store",
          "Retry-After": "60",
        },
      });
    }
  },
};

export class OAuthSession {
  constructor(state) {
    this.state = state;
  }

  async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/callback" && request.method === "POST") return this.storeResult(request);
    if (url.pathname === "/poll" && request.method === "GET") return this.pollResult(request);
    return new Response("Not found\n", { status: 404, headers: oauthHeaders("text/plain; charset=utf-8") });
  }

  async storeResult(request) {
    if (await this.state.storage.get("result")) {
      return new Response("OAuth result already stored\n", { status: 409, headers: oauthHeaders("text/plain; charset=utf-8") });
    }
    let input;
    try {
      input = await request.json();
    } catch {
      return new Response("Invalid JSON\n", { status: 400, headers: oauthHeaders("text/plain; charset=utf-8") });
    }
    if (!input || !/^[A-Za-z0-9_-]{43}$/.test(input.commitment || "") || typeof input.code !== "string" || typeof input.error !== "string" || typeof input.errorDescription !== "string") {
      return new Response("Invalid OAuth result\n", { status: 400, headers: oauthHeaders("text/plain; charset=utf-8") });
    }
    await this.state.storage.put("result", input);
    await this.state.storage.setAlarm(Date.now() + oauthSessionLifetimeMs);
    return new Response(null, { status: 204, headers: oauthHeaders() });
  }

  async pollResult(request) {
    const expected = request.headers.get("X-Vastora-OAuth-Commitment") || "";
    const authorization = request.headers.get("Authorization") || "";
    const secret = authorization.startsWith("Bearer ") ? authorization.slice(7) : "";
    const actual = secret ? await sha256Base64URL(secret) : "";
    if (!constantTimeEqual(actual, expected)) {
      return new Response("Unauthorized\n", { status: 401, headers: oauthHeaders("text/plain; charset=utf-8") });
    }
    const result = await this.state.storage.get("result");
    if (!result) return Response.json({ status: "pending" }, { status: 202, headers: oauthHeaders() });
    if (!constantTimeEqual(result.commitment, expected)) {
      return new Response("Unauthorized\n", { status: 401, headers: oauthHeaders("text/plain; charset=utf-8") });
    }
    await this.state.storage.deleteAll();
    if (result.error) {
      return Response.json({ status: "failed", error: result.error, errorDescription: result.errorDescription }, { status: 400, headers: oauthHeaders() });
    }
    return Response.json({ status: "authorized", code: result.code }, { headers: oauthHeaders() });
  }

  async alarm() {
    await this.state.storage.deleteAll();
  }
}

async function sha256Base64URL(value) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  let binary = "";
  for (const byte of new Uint8Array(digest)) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function constantTimeEqual(left, right) {
  if (left.length !== right.length || left.length === 0) return false;
  let mismatch = 0;
  for (let index = 0; index < left.length; index += 1) mismatch |= left.charCodeAt(index) ^ right.charCodeAt(index);
  return mismatch === 0;
}
