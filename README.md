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

- **Sub-keys in one click.** `ocgr_…` secrets are shown once and stored only as SHA-256 hashes.
- **Live usage accounting.** Every request records input/output/reasoning/cache tokens, latency, model, status and the exact `opencode.allowance` reported by Go.
- **Rolling limits.** Cap **spend (USD)**, **requests**, or **tokens** over any interval: 5 hours, 24 hours, 7 days, 30 days, or a custom window up to 90 days. Multiple limits per key are combined.
- **Model allow-lists.** Grant all models, or a hand-picked subset. Enforced before the request reaches the upstream.
- **Plan visibility.** Per-model 5h / 7d / 30d progress against your Go or Go Plus allowance, so you can see where the plan is going.
- **All four API families.** OpenAI Chat Completions, OpenAI Responses, Anthropic Messages and Gemini pass through untouched, including streaming.
- **Session-header passthrough.** `x-opencode-session` (plus the client's native session headers) is preserved; if a client omits it, the router derives a stable session ID so prompt caching still works.
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

1. Open **Settings → Client setup** in the dashboard.
2. Copy the generated `opencode.jsonc` to `~/.config/opencode/opencode.jsonc`.
3. Export the sub-key:

   ```bash
   export OPENCODE_GO_KEY="ocgr_..."
   ```

4. Install and run opencode v2:

   ```bash
   curl -fsSL https://opencode.ai/v2/install | bash
   opencode run --model go/kimi-k3 "hello"
   ```

The generated config defines a single provider, `go`, with all models. Each model gets the correct runtime package and base URL automatically:

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
- Limits are soft at the moment of overshoot: a request already in flight can push usage past the cap; the next request is blocked.
- Revoking, disabling, editing limits or model access takes effect on the next request. Sub-key secrets are never cached.
- Usage history is retained for `RETENTION_DAYS` (default 180).

## Security

- Sub-keys are 256-bit random secrets; only SHA-256 hashes are stored. Rotation invalidates the old secret immediately.
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
| `PUBLIC_URL` | derived | Base URL embedded in generated client configs. |
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

## Notes & limits of this design

- **Single instance.** State lives in SQLite and an in-memory rolling tracker. Scale vertically, not horizontally. Do not run multiple replicas against the same database.
- **Rolling windows, not billing cycles.** Plan headroom is shown as 5h/7d/30d rolling usage. OpenCode's own monthly allowance resets on the Console billing cycle; treat the plan view as a close estimate, not an invoice.
- **No request rewriting.** Apart from adding `stream_options.include_usage` when a chat client omits it (needed for accounting), bodies are forwarded byte-for-byte. Request bodies may be plain JSON or gzip; do not enable zstd request encoding in a custom provider.
- **Go plan terms.** Sub-keys share your single Go subscription. Make sure the people you invite understand OpenCode's acceptable-use rules.
