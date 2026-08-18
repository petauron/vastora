# CPA app package

This directory documents the first official Vastora app package. The canonical
catalog entry is in `../../catalog.json` so the v0.1 signing command receives
one self-contained JSON payload.

The application image is pinned by digest. Cloudflare Tunnel is outside the
current typed installation flow. The Agent owns `auths`, `logs`, and `plugins`
as runtime data; these paths are not Center configuration and are not uploaded.
