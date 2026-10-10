# UI images

The README's screenshots are generated from the running application, not
hand-made. They are checked in so the README renders on a fresh clone and on
GitHub.

| File | What it shows |
|---|---|
| `tui-session.svg` | The TUI answering a question about a small Go project, with a tool call, a diff-style code block and the token summary. |
| `web-fleet.png` | The Web Studio fleet dashboard, served by `webbridge`. |

## Regenerating

```bash
cd scripts/uishot
npm install && npx playwright install chromium
cd ../..
UISHOT_MODEL=qwen2.5-coder:7b bash scripts/uishot/generate.sh
```

`generate.sh` builds `marshal` and `webbridge`, runs the TUI against a
throwaway demo project, and screenshots the bridge. It uses a temp
`MARSHAL_DATA_DIR`, its own bridge state directory, and a copy of
`config.example.toml` — your real config, database and trust store are not
touched, and nothing is written into the repository outside `docs/ui/`.

Set `UISHOT_MODEL` to a model your provider actually serves. It defaults to
`qwen2.5-coder:7b` against Ollama on `localhost:11434`; edit
`scripts/uishot/config.example.toml` to use a different provider.

## Why it is built this way

**The TUI is captured through a real VT emulator, not an ANSI stripper.**
Marshal is a full-screen Bubble Tea app: it positions the cursor and redraws
diffs in place. Tools that merely strip escape codes (`freeze`, most
"screenshot your terminal" utilities) ignore cursor addressing and produce
concatenated garbage. `scripts/uishot/capture.py` records the raw escape
stream from a PTY, and `render.mjs` replays it through `@xterm/headless` —
a real VT parser — then walks the resulting cell grid to emit SVG.

The SVG contains live `<text>`, so it scales losslessly and stays selectable.
The font is embedded as base64 WOFF2 so columns do not drift in viewers that
would otherwise substitute a different monospace face. (Without embedding,
the same file rendered with a different mono font differs by ~4% RMSE.)

**The Web Studio is captured with a real browser.** The SPA keeps a
Server-Sent Events stream open for the whole session, so waiting for
`networkidle` always times out — the capture waits for the app shell to paint
instead. The bearer token is injected via `addInitScript` before the app
boots, so the screenshot shows the UI rather than the token prompt.

## Requirements

- Go with cgo (the tree-sitter symbol index needs a C toolchain)
- Python 3 (standard library only)
- Node 18+ and Chromium via Playwright
