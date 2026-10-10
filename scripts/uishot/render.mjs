#!/usr/bin/env node
// Render a captured Marshal TUI stream to SVG.
//
// The stream comes from capture.py. We replay it through @xterm/headless, a
// real VT emulator, and then walk the resulting cell grid to emit an SVG.
//
// Why not `freeze` (or any ANSI-stripping tool): those are text formatters,
// not terminal emulators. They ignore cursor addressing, so a full-screen
// Bubble Tea app — which positions the cursor and redraws diffs in place —
// comes out as concatenated garbage. Verified: feeding
// "AAAAAAAA\n ESC[1A BBBBBBBB" to freeze yields BOTH lines, but a real
// terminal shows only the second.
//
// The output is real text, so it scales losslessly and stays selectable. To
// keep columns aligned in viewers that lack the font, the font is embedded
// (see --font). Without that, an unknown viewer substitutes a different
// monospace face and the grid drifts by a few percent.
//
// Usage:
//   render.mjs STREAM.bin OUT.svg [--cols 120] [--rows 38]
//              [--font path/to/font.woff2] [--no-font]
//              [--fg '#ece9e4'] [--bg '#121113']

import fs from 'fs';
import { createRequire } from 'module';

const require = createRequire(import.meta.url);
const { Terminal } = require('@xterm/headless');

// ---- args -----------------------------------------------------------------
const argv = process.argv.slice(2);
if (argv.length < 2) {
  console.error('usage: render.mjs STREAM.bin OUT.svg [--cols N] [--rows N] [--font FILE]');
  process.exit(2);
}
const [streamPath, outPath] = argv;
function opt(name, dflt) {
  const i = argv.indexOf('--' + name);
  return i >= 0 ? argv[i + 1] : dflt;
}
const has = (name) => argv.includes('--' + name);

const COLS = Number(opt('cols', 120));
const ROWS = Number(opt('rows', 38));
const FONT_FILE = opt('font', '');
const DEFAULT_FG = opt('fg', '#ece9e4');
const DEFAULT_BG = opt('bg', '#121113');

// Cell metrics. The font is monospaced, so every cell is the same width;
// 8.4px at a 14px font size matches Geist Mono's advance width closely
// (measured: 119 cells render 1350px vs 1356px expected, 0.4% off).
const FONT_SIZE = 14;
const CW = 8.4;
const CH = 17;
const PAD = 16;

// ---- palette --------------------------------------------------------------
// The xterm 256-colour cube, so 256-colour SGR from the app maps exactly.
// Indices 0-15 are the ANSI palette.
const ANSI16 = [
  '#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5',
  '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff',
];
function palette256(i) {
  if (i < 16) return ANSI16[i];
  if (i < 232) {
    const n = i - 16;
    const lv = (v) => (v === 0 ? 0 : 55 + v * 40);
    return `rgb(${lv(Math.floor(n / 36))},${lv(Math.floor((n % 36) / 6))},${lv(n % 6)})`;
  }
  const v = 8 + (i - 232) * 10;
  return `rgb(${v},${v},${v})`;
}

function resolveColor(mode, value, isFg) {
  // mode: 0 = default, 1 = palette, 2 = RGB
  if (mode === 0 || value < 0) return isFg ? DEFAULT_FG : null;
  if (mode === 2) {
    return `rgb(${(value >> 16) & 255},${(value >> 8) & 255},${value & 255})`;
  }
  return palette256(value);
}

// ---- replay the stream ----------------------------------------------------
const raw = fs.readFileSync(streamPath);
const term = new Terminal({
  cols: COLS,
  rows: ROWS,
  scrollback: 1000,
  // The per-cell colour accessors are behind this flag.
  allowProposedApi: true,
  theme: { foreground: DEFAULT_FG, background: DEFAULT_BG },
});
await new Promise((resolve) => term.write(raw.toString('utf8'), resolve));

const buf = term.buffer.active;

// ---- collect runs ---------------------------------------------------------
// A "run" is a horizontal stretch of cells sharing one attribute set. Emitting
// one <text> per run keeps the file small and the output selectable.
const runs = [];
const bgRects = [];

for (let y = 0; y < ROWS; y++) {
  const line = buf.getLine(buf.baseY + y);
  if (!line) continue;
  let cur = null;
  for (let x = 0; x < COLS; x++) {
    const cell = line.getCell(x);
    if (!cell) continue;
    const ch = cell.getChars();
    const bold = !!cell.isBold();
    const dim = !!cell.isDim();
    const italic = !!cell.isItalic();
    const underline = !!cell.isUnderline();
    const inverse = !!cell.isInverse();

    let fg = resolveColor(cell.getFgColorMode(), cell.getFgColor(), true);
    let bg = resolveColor(cell.getBgColorMode(), cell.getBgColor(), false);
    if (inverse) {
      const t = fg;
      fg = bg || DEFAULT_BG;
      bg = t || DEFAULT_FG;
    }
    if (dim && !inverse) fg = 'rgba(236,233,228,0.6)';

    const key = `${fg}|${bg}|${bold}|${italic}|${underline}`;
    if (!cur || cur.key !== key) {
      cur = { y, x, key, text: '', fg, bg, bold, italic, underline };
      runs.push(cur);
    }
    // A double-width character occupies two cells; the second returns ''.
    if (ch !== '') cur.text += ch;
  }
}

// Backgrounds become rectangles. Only cells with visible ink get one, so an
// empty screen does not emit a rectangle per row.
for (const r of runs) {
  if (r.bg && r.text.trim() !== '') {
    bgRects.push({ x: r.x, y: r.y, w: r.text.length, bg: r.bg });
  }
}

// ---- emit SVG -------------------------------------------------------------
const W = COLS * CW + PAD * 2;
const H = ROWS * CH + PAD * 2;
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

let fontStyle = '';
let fontFamily = "ui-monospace, 'DejaVu Sans Mono', Menlo, monospace";
if (FONT_FILE && !has('no-font')) {
  const b64 = fs.readFileSync(FONT_FILE).toString('base64');
  // The <style> element must be the first child INSIDE <svg>. Placing it
  // before the <svg> element makes the document invalid ("extra content at
  // the end of the document") and rsvg-convert refuses to parse it.
  fontStyle =
    `<style>@font-face{font-family:"MarshalCapture";` +
    `src:url(data:font/woff2;base64,${b64}) format("woff2");}</style>`;
  fontFamily = 'MarshalCapture, ui-monospace, monospace';
}

const out = [];
out.push(
  `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" ` +
  `viewBox="0 0 ${W} ${H}" font-family="${fontFamily}" font-size="${FONT_SIZE}">`
);
out.push(fontStyle);
out.push(`<rect width="${W}" height="${H}" fill="${DEFAULT_BG}"/>`);

for (const r of bgRects) {
  out.push(
    `<rect x="${(PAD + r.x * CW).toFixed(1)}" y="${(PAD + r.y * CH).toFixed(1)}" ` +
    `width="${(r.w * CW).toFixed(1)}" height="${CH}" fill="${r.bg}"/>`
  );
}

for (const r of runs) {
  const text = r.text.replace(/\s+$/, '');
  if (!text) continue;
  const attrs = [
    `x="${(PAD + r.x * CW).toFixed(1)}"`,
    `y="${(PAD + r.y * CH + CH * 0.75).toFixed(1)}"`,
    `fill="${r.fg}"`,
  ];
  if (r.bold) attrs.push('font-weight="bold"');
  if (r.italic) attrs.push('font-style="italic"');
  if (r.underline) attrs.push('text-decoration="underline"');
  out.push(`<text ${attrs.join(' ')} xml:space="preserve">${esc(text)}</text>`);
}

out.push('</svg>');
fs.writeFileSync(outPath, out.join('\n'));
console.error(
  `wrote ${outPath} (${COLS}x${ROWS}, ${runs.length} runs, ${bgRects.length} bg rects)`
);
