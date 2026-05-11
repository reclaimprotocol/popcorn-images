// Shared helpers for stealth probes.
//
// Each probe imports `connect()` to attach to the running container's
// chromium over CDP, plus `humanize()` to inject realistic mouse/scroll
// behavior. Akamai/Cloudflare both score the absence of behavioral signals
// as bot-like, so any probe that hits a sensored site must call humanize()
// after navigation.

import { chromium } from 'playwright-core';
import { readFileSync, existsSync } from 'node:fs';

const CDP_URL = process.env.CDP_URL || 'http://127.0.0.1:9223';
const CREDS_PATH = process.env.PROXY_CREDS || '/tmp/proxy-creds.json';

export async function connect({ proxyAuth = false } = {}) {
  const browser = await chromium.connectOverCDP(CDP_URL);
  const ctx = browser.contexts()[0];
  const page = ctx.pages()[0] || await ctx.newPage();

  if (proxyAuth && existsSync(CREDS_PATH)) {
    const creds = JSON.parse(readFileSync(CREDS_PATH, 'utf8'));
    const cdp = await ctx.newCDPSession(page);
    await cdp.send('Fetch.enable', { handleAuthRequests: true, patterns: [{urlPattern: '*'}] });
    cdp.on('Fetch.requestPaused', async (e) => {
      try { await cdp.send('Fetch.continueRequest', { requestId: e.requestId }); } catch {}
    });
    cdp.on('Fetch.authRequired', async (e) => {
      try {
        await cdp.send('Fetch.continueWithAuth', {
          requestId: e.requestId,
          authChallengeResponse: {
            response: 'ProvideCredentials',
            username: creds.username,
            password: creds.password,
          },
        });
      } catch {}
    });
    console.log('[utils] proxy auth handler installed');
  }

  return { browser, ctx, page };
}

// Approximates a human looking at a page: mouse moves with multi-step
// interpolation (Akamai watches inter-event timing), smooth scrolls
// (sensor records scroll deltas), dwell pauses. Wrapped in try/catch
// so navigation races don't tank the probe.
export async function humanize(page, { rounds = 4, dwell = 1500 } = {}) {
  for (let i = 0; i < rounds; i++) {
    try {
      await page.mouse.move(200 + i*100, 200 + i*80, { steps: 12 });
      await page.evaluate(y => window.scrollTo({top: y, behavior: 'smooth'}), 200 + i*250).catch(()=>{});
      await page.waitForTimeout(dwell);
    } catch {}
  }
  await page.waitForTimeout(4000);
}

export async function readCookie(ctx, host, name) {
  const cookies = await ctx.cookies(host);
  return cookies.find(c => c.name === name) || null;
}

// Parse Akamai _abck verdict token: cookie format is
//   <sensorId>~<verdict>~<sensor>~<v2>~<v3>~<expiry>~<challenge>~<refresh>
// Token "0" = PASS, "-1" = BOT/pending, "8" = invalid sensor.
export function parseAbck(cookie) {
  if (!cookie) return null;
  const m = cookie.value.match(/~([\-0-9]+)~/);
  return { token: m ? m[1] : null, len: cookie.value.length };
}

export function pass(token)  { return token === '0'; }
export function fail(token)  { return token === '-1' || token === null; }

export function section(label) {
  const bar = '─'.repeat(Math.max(0, 60 - label.length));
  console.log(`\n── ${label} ${bar}`);
}

export function summary(rows) {
  console.log('\n══ SUMMARY ════════════════════════════════════════════════');
  const w = Math.max(...rows.map(r => r.name.length));
  for (const r of rows) {
    const mark = r.pass ? '✓' : r.warn ? '!' : '✗';
    console.log(`  ${mark}  ${r.name.padEnd(w)}  ${r.detail || ''}`);
  }
  console.log('═══════════════════════════════════════════════════════════');
}
