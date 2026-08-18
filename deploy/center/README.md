# Install Center and built-in Headscale

The guided installer validates Docker, the TLS certificate, the private key,
ports, the immutable Center image, and the final Compose configuration before
starting anything. It does not install Docker or change DNS.

Before starting, point the Center and Headscale hostnames at this server and
prepare one trusted TLS certificate that covers both names. Then run:

```sh
cd deploy/center
./setup.sh \
  --image 'ghcr.io/petauron/vastora-center@sha256:replace-with-release-digest' \
  --center-url 'https://center.example.com' \
  --headscale-url 'https://headscale.example.com:8443' \
  --tls-cert /path/to/fullchain.pem \
  --tls-key /path/to/private-key.pem
```

The script starts the stack and saves a newly generated Headscale API key in
`generated/headscale-api-key.txt` with mode `0600`.

Next:

1. Open the Center URL and create the first administrator account.
2. Choose **Network → Headscale → Set up**.
3. Keep **Built into Center stack**, enter the Headscale URL, and paste the API
   key from `generated/headscale-api-key.txt`.

Center verifies Headscale before saving the encrypted key. The key field can be
left blank on later edits. Agent join commands contain a one-hour, single-use
key and should be run only on the intended node.

To validate a later Headscale configuration change before restarting:

```sh
docker compose run --rm headscale configtest
```
