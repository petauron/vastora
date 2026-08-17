# Center and built-in Headscale

This product deployment keeps Center and Headscale as separate services with
independent persistent volumes. Center writes the built-in Headscale DNS file
to the shared `center-data` volume; Headscale mounts that file read-only.

1. Copy `.env.example` to `.env` and pin the Center image.
2. Put the TLS certificate and key at `tls/tls.crt` and `tls/tls.key`. The
   certificate must cover both the Center and Headscale hostnames.
3. Edit `headscale/config.yaml`: replace `headscale.example.com`, and change
   the port if `VASTORA_HEADSCALE_PORT` is not `8443`.
4. Start the stack with `docker compose up -d`.
5. Create the Headscale API key once:

   ```sh
   docker compose exec headscale headscale apikeys create --expiration 365d
   ```

6. In Center, choose **Network → Headscale → Built-in**, enter the exact
   `server_url` and the API key. Center verifies HTTPS before saving it.

The API key is encrypted by Center and is never returned by list APIs. Agent
join commands contain a one-time key valid for one hour, so copy them only to
the intended node. Validate configuration changes before restart with:

```sh
docker compose run --rm headscale configtest
```
