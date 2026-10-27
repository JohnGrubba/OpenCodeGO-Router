# OpenCode Go Router

A self-hosted gateway and dashboard for an **OpenCode Go** subscription. It lets you create sub-keys for friends, track exactly what they use, cap their spend or traffic over rolling windows, and restrict which models they can reach — while they use the **official opencode v2 client** without patching anything.

```
┌─────────────────┐     ocgr_…      ┌──────────────────┐     oc_sk_…      ┌──────────────────┐
│ opencode v2     │ ──────────────▶ │  Go Router :4000 │ ───────────────▶ │ opencode.ai      │
│ (or any SDK)    │ ◀────────────── │  keys + limits   │ ◀─────────────── │ /inference/go/…  │
└─────────────────┘   SSE / JSON    └──────────────────┘   usage + cost    └──────────────────┘
                                          │
                                    SQLite + web UI
```

## Features

- **Sub-keys in one click.** `ocgr_…` secrets are shown once and stored only as SHA-256 hashes. The generated client config has the key baked in, so friends do not have to set environment variables.
- **Live usage accounting.** Every request records input/output/reasoning/cache tokens, latency, model, status and the exact `opencode.allowance` reported by Go.
- **Rolling limits.** Cap **spend (USD)**, **requests**, or **tokens** over any interval: 5 hours, 24 hours, 7 days, 30 days, or a custom window up to 90 days. Multiple limits per key are combined.
- **Model allow-lists.** Grant all models, or a hand-picked subset. Enforced before the request reaches the upstream.
- **Plan visibility.** Per-model 5h / 7d / 30d progress against your Go or Go Plus allowance, so you can see where the plan is going.
- **All four API families.** OpenAI Chat Completions, OpenAI Responses, Anthropic Messages and Gemini pass through untouched, including streaming.
- **Session-header passthrough.** `x-opencode-session` (plus the client's native session headers) is preserved; if a client omits it, the router derives a stable session ID so prompt caching still works.
- **Finished per-key configs.** The dashboard emits a ready-to-use `opencode.jsonc` with the secret embedded in both `settings.apiKey` and provider headers, and `small_model` pinned to an allowed model, so OpenCode's title-generation requests never hit a blocked model.
- **Production defaults.** SQLite WAL, connection pooling, streaming flushes, hashed admin sessions, CSRF protection, login throttling, strict security headers, a non-root read-only container.
- **No external services.** The dashboard is embedded in the Go binary. One container, one volume.

## Quick start

Requirements: Docker with the Compose plugin.

```bash
git clone <this-repo> opencode-go-router
cd opencode-go-router
cp .env.example .env
```

Edit `.env`:

```dotenv
OPENCODE_API_KEY=oc_sk_...          # from https://opencode.ai/console
ADMIN_PASSWORD=a-long-password-here # 12+ chars, or leave blank for a generated one
PUBLIC_URL=http://192.168.1.20:4000 # optional, but set this for friends
```

Then start it:

```bash
docker compose up -d --build
docker compose logs -f router
```

Open `http://<server>:4000`, sign in with `ADMIN_PASSWORD`, and create your first key.

The container uses `restart: unless-stopped`. To make sure Docker itself comes back after a reboot:

```bash
sudo systemctl enable --now docker
```

### If you do not set `ADMIN_PASSWORD`

On first boot the router generates a temporary password and prints it once in the logs:

```bash
docker compose logs router | grep -A2 "temporary password"
```

Sign in and change it in **Settings**. The environment variable is only used to bootstrap the first admin; it does not overwrite a password changed in the UI.

### Forgot the admin password

Set `ADMIN_PASSWORD_RESET=true` and a new `ADMIN_PASSWORD` in `.env`, restart, then set `ADMIN_PASSWORD_RESET=false` again:

```bash
docker compose up -d
docker compose logs -f router
```

All dashboard sessions are signed out on reset.

## Connect the official opencode v2 client

No environment variables are needed. When you create or rotate a key, the dashboard shows a finished `opencode.jsonc` with that key already embedded in the provider settings. Use **Download config** in the one-time dialog.

1. Open **Keys → Create key**.
2. Download `opencode.jsonc` and save it as `~/.config/opencode/opencode.jsonc`.
3. Install and run opencode v2:

   ```bash
   curl -fsSL https://opencode.ai/v2/install | bash
   opencode run --model go/kimi-k3 "hello"
   ```

For an existing key whose config file is lost, open the key and choose **Client config → Rotate & get config**. This issues a new secret and a fresh finished config; the old secret stops working.

The generated config defines a single provider, `go`, with all models allowed for that key. Each model gets the correct runtime package and base URL automatically:

| Models | Upstream family | Client package | Router base URL |
| --- | --- | --- | --- |
| GLM, Kimi, DeepSeek, MiMo, MiniMax Flash, Qwen*, Hy, LongCat, Step, … | OpenAI Chat | `openai-compatible` | `/go/openai/v1` |
| GPT, Grok, Muse Spark | OpenAI Responses | `openai` | `/go/openai/v1` |
| Claude Haiku, MiniMax M3, Qwen3.8 Max/Flash, Qwen3.7 Plus | Anthropic Messages | `anthropic` | `/go/anthropic/v1` |
| Gemini 4 Argon | Gemini | `google` | `/go/google/v1beta` |

The config also sets `canonical: "opencode-go"`, so context limits, pricing, reasoning options and tool capabilities come from OpenCode's built-in catalog. It pins `small_model` (used for session titles and other auxiliary work) to an allowed model, so a key restricted to, say, Kimi does not fail when OpenCode asks for a title.

> If `PUBLIC_URL` is not set, the config uses the host you are currently browsing from. Set `PUBLIC_URL` when the dashboard is opened via `localhost` but friends connect over a LAN or domain.

### Any other client

The router mirrors the upstream paths exactly:

| API | Path |
| --- | --- |
| OpenAI Chat | `http://host:4000/go/openai/v1/chat/completions` |
| OpenAI Responses | `http://host:4000/go/openai/v1/responses` |
| Anthropic Messages | `http://host:4000/go/anthropic/v1/messages` |
| Gemini | `http://host:4000/go/google/v1beta/models/<model>:generateContent` |
| Models | `http://host:4000/go/v1/models` |

Convenience aliases also work: `/v1/chat/completions`, `/v1/responses`, `/v1/messages`, `/v1beta/...`, `/openai/v1/...`, `/anthropic/v1/...`, `/google/v1beta/...`.

Authentication accepts all common styles:

```http
Authorization: Bearer ocgr_...
X-Api-Key: ocgr_...
X-Goog-Api-Key: ocgr_...      # Gemini clients
?key=ocgr_...                  # Gemini fallback
```

Everything else is passed through: `anthropic-version`, `anthropic-beta`, trace headers, tool definitions, images, thinking/reasoning options and streaming.

## How limits work

Limits are **rolling windows** evaluated before every request:

```text
spend $5.00 / 24h
requests 1000 / 5h
tokens 2,000,000 / 7d
```

- A key is blocked if **any** limit is exhausted. The upstream returns a family-appropriate `429` with `Retry-After` and `X-Go-Router-Limit-*` headers.
- Spend uses the `opencode.allowance` value from Go (the amount counted against your plan). If a response lacks it, the router falls back to `list_cost` and then to a catalog-price estimate (flagged as estimated).
- **DeepSeek peak pricing is respected.** Go's `allowance` and `list_cost` already include peak pricing. The router's local fallback estimate applies the published 2× multiplier for DeepSeek V4.1 Flash / V4 Pro during **01:00–04:00 and 06:00–10:00 UTC, Monday–Friday**; all other hours and weekends are off-peak. The Models page also documents these windows.
- Limits are soft at the moment of overshoot: a request already in flight can push usage past the cap; the next request is blocked.
- Revoking, disabling, editing limits or model access takes effect on the next request. Sub-key secrets are never cached.
- Usage history is retained for `RETENTION_DAYS` (default 180).

## Security

- Sub-keys are 256-bit random secrets; only SHA-256 hashes are stored. Rotation invalidates the old secret immediately.
- A downloaded client config contains the sub-key in plaintext. Treat it like a password and `chmod 600 ~/.config/opencode/opencode.jsonc`. Finished configs are shown only at creation or rotation because the server cannot recover the secret.
- Admin passwords use bcrypt. Sessions are random 256-bit tokens stored hashed, sent as `HttpOnly`, `SameSite=Lax` cookies; `Secure` is set automatically on HTTPS.
- Every mutating admin API call requires a custom CSRF header and a matching `Origin`.
- Login attempts are throttled per IP. Proxy requests never log prompts or secrets.
- The container runs as UID 10001 with a read-only root filesystem, all capabilities dropped and `no-new-privileges`.
- The master OpenCode key never leaves the server. The dashboard only shows whether it is configured.

For internet exposure, put the router behind a TLS reverse proxy (Caddy, nginx, Traefik), bind the port to localhost (`127.0.0.1:4000:4000`), and set `TRUST_PROXY=true` so client IPs, secure cookies and generated URLs are correct. Example Caddyfile:

```caddyfile
go.example.com {
    reverse_proxy 127.0.0.1:4000
}
```

Then set `PUBLIC_URL=https://go.example.com` in `.env` and restart.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENCODE_API_KEY` | — | Your Go/personal key. Required for proxying. |
| `ADMIN_PASSWORD` | generated | First-boot dashboard password (12+ chars). |
| `PUBLIC_URL` | derived | Base URL embedded in generated client configs. Use the final `https://` URL when a proxy redirects HTTP to HTTPS. |
| `PLAN` | `go` | `go` or `go-plus`; used for plan headroom views. |
| `RETENTION_DAYS` | `180` | Request-log retention (kept at least as long as the longest limit window). |
| `TRUST_PROXY` | `false` | Trust `X-Forwarded-*` headers. Only enable behind a trusted proxy. |
| `SESSION_TTL_HOURS` | `168` | Admin session lifetime. |
| `MODELS_REFRESH_MINUTES` | `15` | Live model-list refresh interval. |
| `MAX_BODY_MB` | `64` | Maximum request body size. |
| `LOG_FORMAT` | `text` | `text` or `json`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `UPSTREAM_BASE_URL` | `https://opencode.ai/inference` | Upstream host override. |

Plan and retention can also be changed in **Settings**.

## Backups

The database lives in the named volume `opencode-go-router-data`. Stop the service first so SQLite checkpoints its WAL cleanly:

```bash
docker compose stop router
docker run --rm \
  -v opencode-go-router-data:/data \
  -v "$PWD:/backup" \
  alpine tar czf /backup/opencode-go-router-backup.tar.gz -C /data .
docker compose start router
```

Restore by extracting into the volume while the container is stopped.

## Development

```bash
go run ./cmd/router        # needs OPENCODE_API_KEY + ADMIN_PASSWORD
go test ./...
make build                 # bin/router
```

The SQLite driver is pure Go (`modernc.org/sqlite`), so `CGO_ENABLED=0` builds work anywhere.

### Layout

```
cmd/router/            startup, graceful shutdown, maintenance
internal/server/       HTTP routes, admin API, auth, static UI
internal/proxy/        streaming proxy, key auth, limits errors
internal/usage/        usage extraction, SSE parsing, rolling tracker
internal/store/        SQLite schema and queries
internal/models/       live model list + pinned catalog merge
internal/catalog/      models.dev snapshot and Go plan limits
internal/web/static/   embedded dashboard (vanilla JS/CSS)
```

## Troubleshooting

### `Provider request failed with HTTP 404` or `Missing model in request body`

This is usually one of two things:

1. The base URL uses `http://` and a reverse proxy redirects it to `https://`. A 301/302 redirect turns a `POST` into a `GET` and drops the body, so the router receives a bodyless request. Always use the final `https://` URL and set `PUBLIC_URL=https://...` in `.env` so generated configs use it.
2. A stale OpenCode session/background service is still using an old provider config. The router now rejects bodyless requests with a clear `400` and logs `proxied request has no model` instead of forwarding them upstream.

Restart the background service and start a fresh session:

```bash
opencode service restart
opencode run --model go/deepseek-v4.1-flash "hello"
```

Fully quit other OpenCode clients (desktop/IDE/another terminal) before retrying. If the error returns, the router log line includes `endpoint`, `method`, `body_bytes` and `json_keys`, which identifies the stale caller.

### `Missing API key. Use Authorization: Bearer ocgr_... or x-api-key.`

This error comes from the router, not OpenCode. It means the request reached the router without any sub-key. Check these in order:

1. **Regenerate the client config.** Configs downloaded before the latest update used `env` and required `OPENCODE_GO_KEY`. Rebuild the router, then create a key or use **Client config → Rotate & get config** and download the fresh `opencode.jsonc`.
2. **Inspect the file.** It should contain a real `ocgr_…` key and no `env` block:

   ```bash
   grep -E '"env"|"apiKey"|"headers"' ~/.config/opencode/opencode.jsonc
   ```

   The config now embeds the key in both `settings.apiKey` and provider `headers` (`Authorization`, `X-Api-Key`, `X-Goog-Api-Key`). The headers are a fallback for clients that ignore `settings.apiKey`.
3. **Check which config is loaded.**

   ```bash
   opencode debug paths
   opencode debug config
   opencode models | grep '^go/'
   ```

   Remove any project-level `opencode.json`/`opencode.jsonc` that also defines `providers.go`, since it may override the global one.
4. **Reload the client.** The background service can keep the old config:

   ```bash
   opencode reload
   ```

   Or run `opencode run --standalone ...` to bypass the background service.
5. **Reverse proxy.** If the router is behind nginx/Caddy/Traefik, make sure it forwards `Authorization`, `X-Api-Key`, and `X-Goog-Api-Key`. The router accepts any of the three.

If it still fails, share the output of `opencode --version`, `opencode debug config`, and the first lines of the config with `ocgr_…` values redacted.

## Notes & limits of this design

- **Single instance.** State lives in SQLite and an in-memory rolling tracker. Scale vertically, not horizontally. Do not run multiple replicas against the same database.
- **Rolling windows, not billing cycles.** Plan headroom is shown as 5h/7d/30d rolling usage. OpenCode's own monthly allowance resets on the Console billing cycle; treat the plan view as a close estimate, not an invoice.
- **No request rewriting.** Apart from adding `stream_options.include_usage` when a chat client omits it (needed for accounting), bodies are forwarded byte-for-byte. Request bodies may be plain JSON or gzip; do not enable zstd request encoding in the generated provider.
- **Go plan terms.** Sub-keys share your single Go subscription. Make sure the people you invite understand OpenCode's acceptable-use rules.
