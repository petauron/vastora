const repository = "petauron/vastora";
const releaseAssets = new Set([
  "install.sh",
  "vastora-center-install.tar.gz",
  "vastora-center-install.tar.gz.sha256",
]);
const currentCacheKey = new Request("https://vastora-installer.internal/current-release");
const lastKnownCacheKey = new Request("https://vastora-installer.internal/last-known-release");
const versionURL = `https://raw.githubusercontent.com/${repository}/main/version.txt`;

function releaseAssetURL(tag, asset) {
  return `https://github.com/${repository}/releases/download/${encodeURIComponent(tag)}/${asset}`;
}

function cachedSelection(result, maxAge) {
  return Response.json(result, {
    headers: { "Cache-Control": `public, max-age=${maxAge}` },
  });
}

async function requestedRelease() {
  const versionResponse = await fetch(versionURL, {
    headers: { "User-Agent": "vastora-installer-worker" },
  });
  if (!versionResponse.ok) {
    throw new Error(`Version pointer returned ${versionResponse.status}`);
  }

  const version = (await versionResponse.text()).trim();
  if (!/^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error("Version pointer is invalid");
  }

  const tag = `v${version}`;
  const assetResponses = await Promise.all(
    [...releaseAssets].map((asset) =>
      fetch(releaseAssetURL(tag, asset), {
        method: "HEAD",
        redirect: "manual",
        headers: { "User-Agent": "vastora-installer-worker" },
      }),
    ),
  );
  if (!assetResponses.every((response) => response.ok || response.status === 302)) {
    throw new Error("Requested release assets are incomplete");
  }

  return { tag };
}

async function selectedRelease(ctx) {
  const cache = caches.default;
  const cached = await cache.match(currentCacheKey);
  if (cached) return cached.json();

  try {
    const result = await requestedRelease();
    ctx.waitUntil(
      Promise.all([
        cache.put(currentCacheKey, cachedSelection(result, 60)),
        cache.put(lastKnownCacheKey, cachedSelection(result, 604800)),
      ]),
    );
    return result;
  } catch (error) {
    const lastKnown = await cache.match(lastKnownCacheKey);
    if (lastKnown) {
      console.warn("Using the last known installer release", {
        message: error instanceof Error ? error.message : String(error),
      });
      return lastKnown.json();
    }
    throw error;
  }
}

function responseHeaders() {
  return {
    "Cache-Control": "public, max-age=60",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
  };
}

export default {
  async fetch(request, env, ctx) {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("Method not allowed\n", {
        status: 405,
        headers: { ...responseHeaders(), Allow: "GET, HEAD" },
      });
    }

    const url = new URL(request.url);
    if (url.pathname === "/") {
      return Response.redirect(`https://github.com/${repository}`, 302);
    }
    const asset = url.pathname.slice(1);
    if (!releaseAssets.has(asset)) {
      return new Response("Not found\n", { status: 404, headers: responseHeaders() });
    }

    try {
      const release = await selectedRelease(ctx);
      const location = releaseAssetURL(release.tag, asset);
      return new Response(null, {
        status: 302,
        headers: { ...responseHeaders(), Location: location },
      });
    } catch (error) {
      console.error("Unable to select installer release", error);
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
