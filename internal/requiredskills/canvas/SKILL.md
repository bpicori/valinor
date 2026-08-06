---
name: canvas
description: Create interactive React drawings, diagrams, and UIs as single-file HTML pages and share them via Cloudflare Quick Tunnel. Load before visual demos; then open the matching sub-skill.
---

# Canvas

Load when the user wants drawings, diagrams, dashboards, interactive explainers, or any visual UI shareable as a public link.

**Procedure only** — write/edit files with normal tools. No npm. Do **not** auto-kill host processes; the operator who starts server/tunnel owns stop (Ctrl+C).

| Use canvas | Skip canvas |
|------------|-------------|
| Diagrams, demos, dashboards | Raster images via an image generator; plain text answers |

## Fixed ports (canvas identity)

| Service | Port | Purpose |
|---------|------|---------|
| Static file server | **11223** | Serves the project/workspace root → `/canvas/<id>/` |
| cloudflared metrics | **11224** | Discover Quick Tunnel URL from a running process |

Do **not** use random ports for canvas. These ports mark “this is the canvas stack.”

## Architecture

```
paint → canvas/<id>/index.html
http.server on 127.0.0.1:11223
cloudflared → 11223, metrics on 11224
state → canvas/.runtime.json
public → https://*.trycloudflare.com/canvas/<id>/
```

## Sub-skills

| Sub-skill | Path | Load when |
|-----------|------|-----------|
| **cloudflared** | `skills/canvas/cloudflared/SKILL.md` | Preflight, start/reuse server+tunnel, resolve public base URL |
| **paint** | `skills/canvas/paint/SKILL.md` | Create/edit `canvas/<id>/index.html` |

Always read the sub-skill before acting.

## Create vs change

| Intent | Action |
|--------|--------|
| New canvas | New `canvas/<id>/index.html` |
| Change existing | Same `id` → edit file; same URL; hard-refresh |
| Another canvas | New `id`; same server/tunnel serves all paths |

## Workflow (every canvas turn)

1. **cloudflared** preflight: server on **11223**, tunnel (metrics **11224** / `.runtime.json`). Start only what is missing. Never double-bind a port.
2. **paint** create/edit HTML.
3. Resolve public base URL (metrics preferred, else `.runtime.json`).
4. Reply with `https://<host>/canvas/<id>/`.
