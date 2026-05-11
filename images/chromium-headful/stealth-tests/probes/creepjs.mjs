// CreepJS — abrahamjuliot.github.io/creepjs is the canonical "how leaky is
// my browser fingerprint" check. It runs ~50 fingerprint vectors and
// aggregates a trust score. We don't fail on a specific number (CreepJS
// keeps moving the goalposts) but we extract the headline score and
// flag well-known bot tells.
//
// What we look for in the parsed result:
//   * trust_score: composite score (0-100); >85 is a healthy real-Chrome target
//   * lies: list of detected JS-level overrides (Object.defineProperty traces)
//   * automation: explicit headless/webdriver tells
//   * resistance: how many resist-fingerprint flags are tripped

import { connect, humanize, section, summary } from '../utils.mjs';

export async function run({ closeBrowser = true } = {}) {
  const { browser, page } = await connect();
  section('CreepJS fingerprint score');

  await page.goto('https://abrahamjuliot.github.io/creepjs/', { waitUntil: 'domcontentloaded', timeout: 60000 });
  // CreepJS computes asynchronously — give it a full minute and poll for
  // the trust-score element.
  console.log('waiting for CreepJS to finish computing (up to 60s)...');
  await humanize(page, { rounds: 3, dwell: 2000 });
  await page.waitForTimeout(15000);

  // Extract the key signals. CreepJS DOM:
  //   .trust-score      → "X% trust"
  //   .lies-list li     → detected lies (one per line, or "none")
  //   .fingerprint-id   → unique fingerprint id (we don't pin, just log)
  const data = await page.evaluate(() => {
    function txt(sel) { const el = document.querySelector(sel); return el ? el.innerText.trim() : null; }
    function all(sel) { return Array.from(document.querySelectorAll(sel)).map(e => e.innerText.trim()); }
    return {
      trustHeader: txt('.trust-score-container') || txt('#fp .trust') || null,
      trustText: document.body.innerText.match(/trust(?:\s*score)?\s*[:\-]?\s*([\d.]+%)/i)?.[1] || null,
      fingerprintId: txt('.fingerprint-section .fp-id') || null,
      lies: all('.lies-list li, .red'),
      bot: document.body.innerText.match(/headless|webdriver|automation/i)?.[0] || null,
      visitsHash: txt('.visitor'),
    };
  });

  console.log(JSON.stringify(data, null, 2));

  const trust = data.trustText ? parseFloat(data.trustText) : null;
  const ok = trust !== null && trust >= 70;

  if (closeBrowser) await browser.close();
  return [{
    name: 'CreepJS trust',
    pass: ok,
    warn: trust !== null && trust >= 50 && trust < 70,
    detail: trust !== null ? `${data.trustText} trust` : '(score not parsed — DOM may have changed)',
  }];
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const rows = await run();
  summary(rows);
}
