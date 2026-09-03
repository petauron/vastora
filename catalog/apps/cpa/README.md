# CPA app package

This directory documents the first official Vastora app package. The canonical
catalog entry is in `../../catalog.json` so the v0.1 signing command receives
one self-contained JSON payload.

The application image is pinned by digest. The `api` service is the management
surface, while `client-api` is a second logical service on the same private
origin. Center only publishes `client-api` through Cloudflare Tunnel with a
`^/v1(/.*)?$` ingress rule; CPA's client API key remains the public API
authentication boundary and the management page is not routed on that
hostname. The Agent owns `auths`, `logs`, and `plugins` as runtime data; these
paths are not Center configuration and are not uploaded. The catalog `version`
is the Vastora package identity and must be incremented whenever the canonical
manifest changes, even if the pinned upstream image does not change.

Center derives the timezone from the selected Agent's Site and generates
separate management and client API keys. Those values are not catalog inputs;
they remain in encrypted deployment secrets and are delivered only through the
sealed Agent task channel.
