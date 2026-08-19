const repository = "petauron/vastora";
const releaseAssets = new Set([
  "install.sh",
  "vastora-center-install.tar.gz",
  "vastora-center-install.tar.gz.sha256",
]);
const cacheKey = new Request("https://vastora-installer.internal/selected-release");

async function selectedRelease(ctx) {
  const cache = caches.default;
  const cached = await cache.match(cacheKey);
  if (cached) return cached.json();

  const response = await fetch(
    `https://api.github.com/repos/${repository}/releases?per_page=20`,
    {
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": "vastora-installer-worker",
        "X-GitHub-Api-Version": "2022-11-28",
      },
    },
  );
  if (!response.ok) throw new Error(`GitHub releases returned ${response.status}`);

  const releases = await response.json();
  const release = releases.find((candidate) => {
    if (candidate.draft || !/^v[0-9A-Za-z._-]+$/.test(candidate.tag_name)) return false;
    const names = new Set(candidate.assets.map((asset) => asset.name));
    return [...releaseAssets].every((name) => names.has(name));
  });
  if (!release) throw new Error("No complete Vastora release is available");

  const result = { tag: release.tag_name };
  const cachedResponse = Response.json(result, {
    headers: { "Cache-Control": "public, max-age=300" },
  });
  ctx.waitUntil(cache.put(cacheKey, cachedResponse));
  return result;
}

function responseHeaders() {
  return {
    "Cache-Control": "public, max-age=300",
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
      const location = `https://github.com/${repository}/releases/download/${encodeURIComponent(release.tag)}/${asset}`;
      return new Response(null, {
        status: 302,
        headers: { ...responseHeaders(), Location: location },
      });
    } catch (error) {
      console.error("Unable to select installer release", error);
      return new Response("No complete Vastora release is currently available.\n", {
        status: 503,
        headers: { ...responseHeaders(), "Retry-After": "300" },
      });
    }
  },
};
