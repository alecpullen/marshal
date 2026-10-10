#!/usr/bin/env node
// Capture a Marshal Web Studio view as PNG.
//
// Usage:
//   shot.mjs BASE_URL OUT.png [--route '#runs'] [--width 1440] [--height 900]
//            [--token TOK] [--scale 2] [--wait 3500]
//
// Two things about the SPA that are easy to get wrong:
//
// 1. The bearer token lives in sessionStorage under 'marshal:token' (see
//    web/ui/src/lib/api.ts). Without it the app calls window.prompt() and you
//    screenshot a JavaScript dialog instead of the UI. addInitScript runs
//    before any page script, so the token is in place when the app boots.
//
// 2. Never wait for 'networkidle'. The SPA holds a Server-Sent Events stream
//    open for the whole session, so the network is *never* idle and
//    networkidle always times out. Wait for the app shell to paint instead.

import fs from 'fs';
import { chromium } from 'playwright';

const argv = process.argv.slice(2);
const [base, out] = argv;
if (!base || !out) {
  console.error('usage: shot.mjs BASE_URL OUT.png [--route R] [--width W] [--height H] [--token T]');
  process.exit(2);
}
function opt(name, dflt) {
  const i = argv.indexOf('--' + name);
  return i >= 0 ? argv[i + 1] : dflt;
}
const route = opt('route', '');
const width = Number(opt('width', 1440));
const height = Number(opt('height', 900));
const token = opt('token', '');
const scale = Number(opt('scale', 2));
const settle = Number(opt('wait', 3500));

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width, height },
  deviceScaleFactor: scale,
  colorScheme: 'dark',
});

if (token) {
  await ctx.addInitScript((tok) => {
    try { sessionStorage.setItem('marshal:token', tok); } catch { /* private mode */ }
  }, token);
}

const page = await ctx.newPage();
const problems = [];
page.on('console', (m) => {
  if (m.type() === 'error' && !/502|Bad Gateway/.test(m.text())) {
    problems.push(m.text().slice(0, 160));
  }
});
page.on('pageerror', (e) => problems.push('pageerror: ' + String(e).slice(0, 160)));

await page.goto(base + route, { waitUntil: 'domcontentloaded', timeout: 30000 });
await page.waitForSelector('#app > *', { timeout: 20000 }).catch(() => {
  console.error('warning: #app never gained children — is the bundle built?');
});
// Let the fleet SSE deliver a first snapshot so we capture data, not skeletons.
await page.waitForTimeout(settle);

const text = (await page.locator('body').innerText().catch(() => '')) || '';
await page.screenshot({ path: out });
await browser.close();

const size = fs.statSync(out).size;
console.error(`wrote ${out} (${width}x${height} @${scale}x, ${(size / 1024).toFixed(0)} KB)`);
console.error(`body text: ${text.length} chars`);
if (text.trim().length < 40) {
  console.error('warning: page looks empty — check the bridge is running and the token is right');
}
if (problems.length) {
  console.error('--- page errors ---');
  for (const p of problems.slice(0, 6)) console.error(p);
}
