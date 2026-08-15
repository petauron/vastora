# CPA app package

This directory documents the first official Vastora app package. The canonical
catalog entry is in `../../catalog.json` so the v0.1 signing command receives
one self-contained JSON payload.

The two images are pinned by digest. Cloudflare Tunnel is an optional Compose
profile, so core CPA can be validated without a real Tunnel token. The Node
will own `auths`, `logs`, and `plugins` as runtime data; these paths are not
Master configuration and are not uploaded.
