---
name: canvas-paint
description: Paint single-file React canvas pages with CDN React/Tailwind. Load from the canvas skill when creating or editing canvas/<id>/index.html.
---

# Canvas sub-skill: paint

Parent: `skills/canvas/SKILL.md`. Publishing / ports / tunnel: `skills/canvas/cloudflared/SKILL.md`.

## Layout

```text
canvas/<id>/index.html
canvas/.runtime.json    # tunnel state from cloudflared flow — not a page
```

Short slug for `<id>`. One self-contained HTML file. No npm/build.

| Intent | Files |
|--------|--------|
| New canvas | New `id` → full `index.html` |
| Change existing | Same `id` → edit or overwrite |
| Fresh take, keep old | New `id` |

Edits do **not** restart server/tunnel. Browser hard-refresh. Public base URL comes from cloudflared preflight / `canvas/.runtime.json` (origin **11223**, metrics **11224**).

## Skeleton

CDN-only. Head: React, ReactDOM, Babel, Tailwind. Body mounts `<App />`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>…</title>
  <script src="https://cdn.tailwindcss.com"></script>
  <script crossorigin src="https://unpkg.com/react@18.3.1/umd/react.production.min.js"></script>
  <script crossorigin src="https://unpkg.com/react-dom@18.3.1/umd/react-dom.production.min.js"></script>
  <script src="https://unpkg.com/@babel/standalone@7.26.5/babel.min.js"></script>
</head>
<body>
  <div id="root"></div>
  <script type="text/babel" data-presets="react">
    function App() {
      return <div className="p-8">…</div>;
    }
    ReactDOM.createRoot(document.getElementById("root")).render(<App />);
  </script>
</body>
</html>
```

Implement UI in `App`. SVG for drawings. Extra CDNs only if needed. No secrets in HTML.
