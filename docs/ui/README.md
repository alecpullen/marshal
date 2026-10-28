# UI images

The README's screenshots are generated from the running application, not
hand-made. They are checked in so the README renders on a fresh clone and on
GitHub.

| File | What it shows |
|---|---|
| `tui-session.svg` | The TUI explaining a small Go project: file reads, a background test job and a subagent, the answer and the token summary, over the framed console (the tray, the input and the status line as its bottom border). |
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
`qwen2.5-coder:7b` against Ollama on `localhost:11434`. To use another
provider, point `UISHOT_CONFIG` at a different capture config.
`config.lmstudio.toml` is the one for LM Studio. The current image was made
with it:

```bash
lms load qwen/qwen3.5-9b --context-length 32768
UISHOT_CONFIG=scripts/uishot/config.lmstudio.toml UISHOT_SKIP_WEB=1 \
UISHOT_SECONDS=200 \
UISHOT_KEYS='6.0:Run go test ./... in the background and explain what calc.go and calc_test.go do,0.8:\r' \
  bash scripts/uishot/generate.sh
```

- `UISHOT_SKIP_WEB=1` regenerates only the TUI image and leaves
  `web-fleet.png` alone.
- `UISHOT_KEYS` is the keystroke schedule: comma-separated `DELAY:text`
  steps, so the question itself cannot contain a comma.
- `UISHOT_SECONDS` is how long to record. The image is the last screen, so
  allow for the whole turn.

A small local model does not always take the same path, and a run that
shows a failed tool call is not one to keep. Look at the image before you
commit it, and rerun if it shows one.

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

**Box-drawing and block characters are drawn, not typeset.** The embedded
font is a Latin subset with no box-drawing glyphs. As text, the console's
frame would come from whatever fallback face the viewer has: the rounded
corners would turn square and the borders would drift off the grid. So, as a
terminal does, `render.mjs` draws `─ │ ╭ ├ ┆ ▍ █` and their relatives as SVG
lines, arcs and rectangles. Every other non-ASCII glyph (`❯ ● ✓ ⧉ ·`) is
centred on its own cell, so a fallback glyph cannot push the rest of the row
off the grid.

**The Web Studio is captured with a real browser.** The SPA keeps a
Server-Sent Events stream open for the whole session, so waiting for
`networkidle` always times out — the capture waits for the app shell to paint
instead. The bearer token is injected via `addInitScript` before the app
boots, so the screenshot shows the UI rather than the token prompt.

## Requirements

- Go with cgo (the tree-sitter symbol index needs a C toolchain)
- Python 3 (standard library only)
- Node 18+ and Chromium via Playwright
