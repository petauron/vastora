# CPA app package

This directory documents the first official Vastora app package. The canonical
catalog entry is in `../../catalog.json` so the v0.1 signing command receives
one self-contained JSON payload.

The application image is pinned by digest. Cloudflare Tunnel is outside the
current typed installation flow. The Agent owns `auths`, `logs`, and `plugins`
as runtime data; these paths are not Center configuration and are not uploaded.

Center derives the timezone from the selected Agent's Site and generates
separate management and client API keys. Those values are not catalog inputs;
they remain in encrypted deployment secrets and are delivered only through the
sealed Agent task channel.
