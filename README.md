# webhook-test-endpoint

A single-binary webhook testing backend with a web UI. Generate a unique URL,
point your webhook sender at it, and inspect every request live — headers,
payload, auth and signature verification results.

## Quick start

```console
$ go run github.com/stephenc-ori/webhook-test-endpoint@latest
2026/07/08 14:00:00 listening on http://:8080
```

Open `http://localhost:8080/`, click **Generate endpoint**, and you are
redirected to `/{id}/` — the inspector UI for that endpoint. IDs are three
random dictionary words, e.g. `abacus-zebra-canyon`. The webhook receiver
is `/{id}/hook`:

```console
$ curl -X POST -H 'Content-Type: application/json' \
    -d '{"hello":"world"}' http://localhost:8080/{id}/hook
{"status":"success"}
```

The request appears in the UI immediately (Server-Sent Events). The server
retains the last 50 requests per endpoint; the UI additionally keeps
everything it has seen during the browser session. Endpoints live in memory
and expire after 24 hours of inactivity.

## Quick start on a public URL

Webhook senders usually need to reach you from the internet. Two fast ways:

**Cloudflare quick tunnel** — free, no account, exposes a locally running
instance at a random `https://….trycloudflare.com` URL:

```console
$ go run . &
$ cloudflared tunnel --url http://localhost:8080
```

Quick tunnels buffer Server-Sent Events, so live push does not get through;
the UI detects the silent stream and falls back to polling every 5 seconds
(the status dot turns amber).

**Google Cloud Run** — hosted, free tier covers light use, builds straight
from this repo's Dockerfile:

```console
$ gcloud run deploy webhook-test-endpoint --source . \
    --allow-unauthenticated --max-instances 1 --region europe-west1
```

`--max-instances 1` is required: endpoints and events are held in the
memory of a single process. Cloud Run scales to zero when idle, which
discards in-memory endpoints — generate a fresh one after a long pause.

## Endpoint settings

Everything is configured per-endpoint from the **Settings** panel in the UI:

- **Authentication** — none (default), HTTP Basic (username/password), or
  Bearer token. Incoming requests are checked against the credential.
- **Payload signature** — GitHub-style HMAC-SHA256 verification. Set the
  shared secret and (optionally) the header name, default
  `X-Hub-Signature-256`. The header value must be
  `sha256=<hex(HMAC-SHA256(secret, raw body))>`.
- **On failure** — what to do when auth or signature verification fails:
  - *Reject and log* (default): respond 401 (auth) / 403 (signature) and
    record the request, marked as rejected.
  - *Reject silently*: respond 401/403 without recording it.
  - *Accept, but mark*: respond normally; the request is flagged as failed
    in the UI only.
- **Response** — the status code, content type and body returned to the
  sender on success. Defaults to `200` / `application/json` /
  `{"status":"success"}`.
- **Pass-through proxy** — relay requests to another webhook listener. See
  below.

## Pass-through proxy

An endpoint can double as a pass-through proxy to a real webhook listener.
When enabled, every request the endpoint *stores* (i.e. accepted, or accepted
under *Accept, but mark* — rejected and silently-dropped requests are never
relayed) is forwarded live to the destination URL, method/headers/body intact.
Connection-scoped headers (`Host`, `Content-Length`, …) are dropped and a
`X-Forwarded-By: webhook-test-endpoint` marker is added. Delivery is
fire-and-forget: the sender always gets the endpoint's configured response,
and forward failures are logged, not surfaced.

Beyond the live relay, from the request detail pane you can:

- **Re-deliver** any retained request to the destination on demand.
- **Download** a request as a [Bruno](https://www.usebruno.com) `.bru` file.
- **Upload** a `.bru` file (or a raw body) and deliver it to the destination —
  handy for replaying a saved payload without crafting `curl` by hand. Only
  the method, headers and body are read from the `.bru`; the rest of Bruno's
  feature set is ignored.

Because the proxy turns the server into an outbound HTTP client (an SSRF
vector on a public instance), the feature is gated by a **server secret**.
Enabling the proxy, re-delivering and uploading all require it in the
`X-Proxy-Secret` request header; the UI prompts for it in the Settings panel
and remembers it for the browser session. The secret is resolved once at
startup:

1. the `WEBHOOK_PROXY_SECRET` environment variable, if set;
2. otherwise the contents of the file named by `-proxy-secret-file`;
3. otherwise a random secret is generated and logged to the console.

```console
$ WEBHOOK_PROXY_SECRET=hunter2 webhook-test-endpoint
# or
$ webhook-test-endpoint -proxy-secret-file /run/secrets/proxy-secret
```

## TLS

Plain HTTP is the default. Three mutually exclusive HTTPS modes:

```console
# Supplied certificate
$ webhook-test-endpoint -tls-cert cert.pem -tls-key key.pem

# Self-signed certificate generated at startup
$ webhook-test-endpoint -tls-self-signed -tls-hosts localhost,127.0.0.1,myhost.example

# Let's Encrypt (ACME); listens on :443 plus :80 for HTTP-01 challenges
$ webhook-test-endpoint -acme-domain hooks.example.com -acme-email you@example.com
```

In self-signed mode the server generates an in-memory CA and a server
certificate signed by it. The CA certificate is shown PEM-encoded on the
landing page and served at `/ca.pem`, so you can configure your webhook
sender to validate the server certificate instead of disabling verification:

```console
$ curl -sk https://localhost:8080/ca.pem > ca.pem
$ curl --cacert ca.pem -X POST https://localhost:8080/{id}/hook -d '{"a":1}'
```

ACME flags:

| Flag | Purpose |
|---|---|
| `-acme-domain` | Domain to obtain a certificate for; repeatable or comma-separated |
| `-acme-email` | ACME account email (expiry notices) |
| `-acme-cache` | Certificate cache directory (default `~/.cache/webhook-test-endpoint/acme`) |
| `-acme-directory-url` | ACME directory override, e.g. Let's Encrypt staging |
| `-acme-http-addr` | Address of the HTTP-01 challenge/redirect listener (default `:80`) |

`-addr` sets the listen address in every mode (default `:8080`, or `:443`
when ACME is enabled).

## Docker

The image is distroless (`gcr.io/distroless/static`): a static binary plus
TLS root certificates, running as nonroot. ~16 MB.

```console
$ docker build -t webhook-test-endpoint .

# Plain HTTP
$ docker run --rm -p 8080:8080 webhook-test-endpoint

# Self-signed TLS
$ docker run --rm -p 8443:8443 webhook-test-endpoint -addr :8443 -tls-self-signed

# Let's Encrypt; persist the certificate cache across restarts
$ docker run --rm -p 80:80 -p 443:443 -v acme-cache:/data \
    webhook-test-endpoint -acme-domain hooks.example.com -acme-email you@example.com
```

The ACME certificate cache lives under `/data` (via `XDG_CACHE_HOME`);
mount a volume there so certificates survive container restarts.

## Development

```console
$ go test ./...
$ go build .
```

No build step for the frontend — it is plain HTML/CSS/JS embedded in the
binary via `go:embed`. The only dependency is `golang.org/x/crypto` (ACME).

## License

[Apache License 2.0](LICENSE)
