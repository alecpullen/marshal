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
      cur = { y, x, key, text: '', cells: [], width: 0, fg, bg, bold, italic, underline };
      runs.push(cur);
    }
    // A double-width character occupies two cells; the second has width 0.
    // A cell nothing was written to returns '' with width 1: it is a space,
    // and dropping it would shift the rest of the run left.
    const w = cell.getWidth();
    if (w === 0) continue;
    const c = ch === '' ? ' ' : ch;
    cur.text += c;
    cur.cells.push({ x, ch: c, w });
    cur.width += w;
  }
}

// Backgrounds become rectangles. Only cells with visible ink get one, so an
// empty screen does not emit a rectangle per row.
for (const r of runs) {
  if (r.bg && r.text.trim() !== '') {
    bgRects.push({ x: r.x, y: r.y, w: r.width, bg: r.bg });
  }
}

// ---- box drawing ------------------------------------------------------------
// Terminals draw box-drawing and block characters themselves rather than ask
// the font, and so does this. The embedded font is a Latin subset, so a frame
// drawn as text falls back to whatever face the viewer has: the rounded
// corners turn square and the borders drift off the grid.
//
// A line glyph is its four arms from the cell centre, in the order left, up,
// right, down: 0 none, 1 light, 2 heavy.
const LINES = {
  '─': [1, 0, 1, 0], '━': [2, 0, 2, 0], '│': [0, 1, 0, 1], '┃': [0, 2, 0, 2],
  '┌': [0, 0, 1, 1], '┐': [1, 0, 0, 1], '└': [0, 1, 1, 0], '┘': [1, 1, 0, 0],
  '┏': [0, 0, 2, 2], '┓': [2, 0, 0, 2], '┗': [0, 2, 2, 0], '┛': [2, 2, 0, 0],
  '├': [0, 1, 1, 1], '┤': [1, 1, 0, 1], '┬': [1, 0, 1, 1], '┴': [1, 1, 1, 0],
  '┣': [0, 2, 2, 2], '┫': [2, 2, 0, 2], '┳': [2, 0, 2, 2], '┻': [2, 2, 2, 0],
  '┼': [1, 1, 1, 1], '╋': [2, 2, 2, 2],
  '╴': [1, 0, 0, 0], '╵': [0, 1, 0, 0], '╶': [0, 0, 1, 0], '╷': [0, 0, 0, 1],
  '╸': [2, 0, 0, 0], '╹': [0, 2, 0, 0], '╺': [0, 0, 2, 0], '╻': [0, 0, 0, 2],
};
// Dashed lines: direction, number of dashes, weight.
const DASHES = {
  '╌': ['h', 2, 1], '╍': ['h', 2, 2], '╎': ['v', 2, 1], '╏': ['v', 2, 2],
  '┄': ['h', 3, 1], '┅': ['h', 3, 2], '┆': ['v', 3, 1], '┇': ['v', 3, 2],
  '┈': ['h', 4, 1], '┉': ['h', 4, 2], '┊': ['v', 4, 1], '┋': ['v', 4, 2],
};
// Rounded corners: the two arms they join, as for LINES.
const ARCS = { '╭': [0, 0, 1, 1], '╮': [1, 0, 0, 1], '╰': [0, 1, 1, 0], '╯': [1, 1, 0, 0] };
// Block elements: the filled part of the cell as fractions [x, y, w, h].
const BLOCKS = {
  '█': [0, 0, 1, 1], '▀': [0, 0, 1, 1 / 2], '▐': [1 / 2, 0, 1 / 2, 1],
  '▔': [0, 0, 1, 1 / 8], '▕': [7 / 8, 0, 1 / 8, 1],
};
for (let i = 1; i <= 7; i++) {
  BLOCKS[String.fromCodePoint(0x2580 + i)] = [0, 1 - i / 8, 1, i / 8]; // ▁ … ▇
  BLOCKS[String.fromCodePoint(0x2590 - i)] = [0, 0, i / 8, 1]; // ▏ … ▉
}
const SHADES = { '░': 0.25, '▒': 0.5, '▓': 0.75 };

const LIGHT = 1.2;
const HEAVY = 2.6;
const weight = (n) => (n === 2 ? HEAVY : LIGHT);
const f1 = (n) => n.toFixed(1);
const line = (x1, y1, x2, y2, w, fg) =>
  `<line x1="${f1(x1)}" y1="${f1(y1)}" x2="${f1(x2)}" y2="${f1(y2)}" stroke="${fg}" stroke-width="${w}"/>`;

// boxShapes returns the SVG for one box or block glyph whose cell starts at
// (x0, y0), or null when ch is not one this draws.
function boxShapes(ch, x0, y0, fg) {
  const cx = x0 + CW / 2;
  const cy = y0 + CH / 2;
  const arms = LINES[ch];
  if (arms) {
    const [l, u, r, d] = arms;
    // An arm runs past the centre by half the crossing stroke, so a corner
    // or a tee has no notch where its arms meet.
    const hx = u || d ? weight(Math.max(u, d)) / 2 : 0;
    const vy = l || r ? weight(Math.max(l, r)) / 2 : 0;
    const out = [];
    if (l && l === r) out.push(line(x0, cy, x0 + CW, cy, weight(l), fg));
    else {
      if (l) out.push(line(x0, cy, cx + hx, cy, weight(l), fg));
      if (r) out.push(line(cx - hx, cy, x0 + CW, cy, weight(r), fg));
    }
    if (u && u === d) out.push(line(cx, y0, cx, y0 + CH, weight(u), fg));
    else {
      if (u) out.push(line(cx, y0, cx, cy + vy, weight(u), fg));
      if (d) out.push(line(cx, cy - vy, cx, y0 + CH, weight(d), fg));
    }
    return out.join('');
  }
  const dash = DASHES[ch];
  if (dash) {
    const [dir, n, wt] = dash;
    const span = dir === 'h' ? CW : CH;
    const step = span / n;
    const gap = step / 3;
    const out = [];
    for (let i = 0; i < n; i++) {
      const a = i * step + gap / 2;
      const b = (i + 1) * step - gap / 2;
      out.push(dir === 'h'
        ? line(x0 + a, cy, x0 + b, cy, weight(wt), fg)
        : line(cx, y0 + a, cx, y0 + b, weight(wt), fg));
    }
    return out.join('');
  }
  const arc = ARCS[ch];
  if (arc) {
    const [l, u] = arc;
    const rad = CW / 2;
    // From the horizontal arm's edge to the vertical arm's edge, through a
    // quarter circle that ends rad short of the centre on each axis.
    const ex = l ? x0 : x0 + CW;
    const ey = u ? y0 : y0 + CH;
    const sx = l ? -1 : 1;
    const sy = u ? -1 : 1;
    const sweep = l === u ? 0 : 1;
    const d =
      `M${f1(ex)} ${f1(cy)}H${f1(cx + sx * rad)}` +
      `A${f1(rad)} ${f1(rad)} 0 0 ${sweep} ${f1(cx)} ${f1(cy + sy * rad)}` +
      `V${f1(ey)}`;
    return `<path d="${d}" fill="none" stroke="${fg}" stroke-width="${LIGHT}"/>`;
  }
  const blk = BLOCKS[ch];
  if (blk) {
    const [bx, by, bw, bh] = blk;
    return `<rect x="${f1(x0 + bx * CW)}" y="${f1(y0 + by * CH)}" ` +
      `width="${f1(bw * CW)}" height="${f1(bh * CH)}" fill="${fg}"/>`;
  }
  const shade = SHADES[ch];
  if (shade) {
    return `<rect x="${f1(x0)}" y="${f1(y0)}" width="${f1(CW)}" height="${CH}" ` +
      `fill="${fg}" fill-opacity="${shade}"/>`;
  }
  return null;
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

// ASCII comes from the embedded font, whose advance is exactly CW, so a
// stretch of it is one <text>. Anything else may come from a fallback face
// with a different advance, so each such glyph is centred on its own cell
// and cannot push the rest of the row off the grid.
function emitText(r, x, w, text, anchor) {
  const attrs = [
    anchor ? `x="${f1(PAD + (x + w / 2) * CW)}" text-anchor="middle"` : `x="${f1(PAD + x * CW)}"`,
    `y="${f1(PAD + r.y * CH + CH * 0.75)}"`,
    `fill="${r.fg}"`,
  ];
  if (r.bold) attrs.push('font-weight="bold"');
  if (r.italic) attrs.push('font-style="italic"');
  if (r.underline) attrs.push('text-decoration="underline"');
  out.push(`<text ${attrs.join(' ')} xml:space="preserve">${esc(text)}</text>`);
}

// A stretch of ─ or ━ is drawn as one line. Cells are a fractional number of
// pixels wide, so a line per cell leaves anti-aliased seams where they meet;
// the square caps overlap the corner or tee at each end for the same reason.
const isRule = (ch) => ch === '─' || ch === '━';

for (const r of runs) {
  let seg = null;
  let rule = null;
  const flush = () => {
    if (seg) {
      const lead = seg.text.length - seg.text.trimStart().length;
      const text = seg.text.trim();
      if (text) emitText(r, seg.x + lead, 0, text, false);
      seg = null;
    }
    if (rule) {
      const y = PAD + r.y * CH + CH / 2;
      out.push(line(PAD + rule.x * CW, y, PAD + (rule.end + 1) * CW, y, weight(LINES[rule.ch][0]), r.fg)
        .replace('/>', ' stroke-linecap="square"/>'));
      rule = null;
    }
  };
  for (const c of r.cells) {
    if (c.ch.length === 1 && c.ch.charCodeAt(0) < 0x80) {
      if (rule) flush();
      if (!seg) seg = { x: c.x, text: '' };
      seg.text += c.ch;
      continue;
    }
    if (isRule(c.ch)) {
      if (seg || (rule && rule.ch !== c.ch)) flush();
      if (!rule) rule = { x: c.x, ch: c.ch };
      rule.end = c.x;
      continue;
    }
    flush();
    const shapes = boxShapes(c.ch, PAD + c.x * CW, PAD + r.y * CH, r.fg);
    if (shapes) out.push(shapes);
    else emitText(r, c.x, c.w, c.ch, true);
  }
  flush();
}

out.push('</svg>');
fs.writeFileSync(outPath, out.join('\n'));
console.error(
  `wrote ${outPath} (${COLS}x${ROWS}, ${runs.length} runs, ${bgRects.length} bg rects)`
);
