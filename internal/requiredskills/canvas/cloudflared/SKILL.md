---
name: canvas-cloudflared
description: Canvas host stack on ports 11223/11224 — preflight, Quick Tunnel, persist and recover public URL. Load from the canvas skill before sharing links.
---

# Canvas sub-skill: cloudflared

Parent: `skills/canvas/SKILL.md`. HTML: `skills/canvas/paint/SKILL.md`.

Start server and tunnel on the **same machine** that holds the workspace files (the machine that can bind localhost). If tools cannot reach host localhost, run preflight/start commands in a host shell and use the output plus workspace state files.

## Constants

| Name | Value |
|------|--------|
| HTTP origin | `http://127.0.0.1:11223` |
| Serve dir | Workspace / project root so paths are `/canvas/<id>/` |
| Metrics | `http://127.0.0.1:11224/metrics` |
| Runtime state | `canvas/.runtime.json` (relative to workspace root) |

## Sources

- [cloudflare/skills](https://github.com/cloudflare/skills) → tunnel references
- [Quick Tunnels](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/)
- [Install cloudflared](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/)
- Quick Tunnel hostname via metrics: `userHostname` / `cloudflared_tunnel_user_hostnames_counts`

## Always preflight (before start or share)

### 1) Web server on 11223?

```bash
curl -sf -o /dev/null -w "%{http_code}" http://127.0.0.1:11223/ || echo DOWN
lsof -nP -iTCP:11223 -sTCP:LISTEN 2>/dev/null || true
```

| Result | Action |
|--------|--------|
| HTTP response from a server | Reuse — do **not** start another listener on 11223 |
| DOWN + port free | Start server (below) |
| Port busy, not our server | Free 11223 or stop the other process; do not start a second stack |

### 2) Tunnel / metrics on 11224?

```bash
curl -sf http://127.0.0.1:11224/metrics | head -5 || echo DOWN
lsof -nP -iTCP:11224 -sTCP:LISTEN 2>/dev/null || true
```

| Result | Action |
|--------|--------|
| Metrics responds | Tunnel running — recover URL; do **not** start another cloudflared |
| DOWN + port free | Start tunnel (below) |
| Port busy, no metrics | Conflict — free 11224 |

### 3) Resolve public base URL (prefer live process)

**Preferred — cloudflared metrics** (tunnel started with `--metrics 127.0.0.1:11224`):

```bash
curl -sf http://127.0.0.1:11224/metrics \
  | grep -oE 'userHostname="https://[a-zA-Z0-9.-]+\.trycloudflare\.com"' \
  | head -1 \
  | sed -E 's/userHostname="(.*)"/\1/'
```

If the label shape differs:

```bash
curl -sf http://127.0.0.1:11224/metrics | grep -i 'trycloudflare\|userHostname' | head -20
```

When a URL is found: update `canvas/.runtime.json`.

**Fallback — state file:** read `canvas/.runtime.json`. Trust it only if metrics/port still show the tunnel up. If the process is dead, treat the file as stale and refresh after restart.

**Last resort:** copy URL from the cloudflared log line (`Your quick Tunnel has been created!`).

### 4) Persist state

```json
{
  "http_port": 11223,
  "metrics_port": 11224,
  "origin": "http://127.0.0.1:11223",
  "public_base_url": "https://RANDOM.trycloudflare.com",
  "updated_at": "2026-08-06T12:00:00Z"
}
```

Share: `{public_base_url}/canvas/<id>/`  
Local: `http://127.0.0.1:11223/canvas/<id>/`

## Start (only missing pieces)

Install once: `brew install cloudflared` or [downloads](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/).

**Terminal A — static server** (if 11223 down), from the workspace root:

```bash
python3 -m http.server 11223 --bind 127.0.0.1
```

**Terminal B — Quick Tunnel** (if tunnel/metrics down):

```bash
cloudflared tunnel --url http://127.0.0.1:11223 --metrics 127.0.0.1:11224 --no-autoupdate
```

Then recover URL from metrics, write `canvas/.runtime.json`. Retry metrics briefly if empty; else use the log line.

No login, token, or custom domain for Quick Tunnel. If `~/.cloudflared/config.yml` breaks Quick Tunnel, rename it temporarily.

## Stop

Whoever started the processes stops them (Ctrl+C, or kill listeners on 11223/11224). Public links die. Clear or ignore stale `public_base_url` until the next start.

## Do / don’t

| Do | Don’t |
|----|--------|
| Preflight every time | Start a second server on 11223 |
| Prefer metrics for live URL | Trust stale `.runtime.json` if metrics/port are down |
| Reuse one stack for all canvases | Restart tunnel for each edit |
| Persist URL to `canvas/.runtime.json` | Use random ports for canvas |

## Optional: named tunnel + token

Only if a stable hostname is required and a domain is already on Cloudflare. Read Cloudflare tunnel docs, ask the operator for the token. Keep origin on **11223**. Not the default path.
