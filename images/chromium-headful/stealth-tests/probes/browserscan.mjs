// browserscan.net — aggregator that surfaces fingerprint, automation,
// hardware, TLS, and bot signals in a single page. We extract its
// composite "bot detection result" and a short list of per-category
// verdicts.

import { connect, humanize, section, summary } from '../utils.mjs';

export async function run({ closeBrowser = true } = {}) {
  const { browser, page } = await connect();
  section('browserscan.net bot detection');

  await page.goto('https://www.browserscan.net/bot-detection', { waitUntil: 'domcontentloaded', timeout: 45000 });
  await humanize(page, { rounds: 3, dwell: 1500 });
  await page.waitForTimeout(10000);

  const data = await page.evaluate(() => {
    function txt(sel) { const el = document.querySelector(sel); return el ? el.innerText.trim() : null; }
    return {
      verdict: txt('.result-text') || txt('h1') || document.body.innerText.match(/(normal|bot|abnormal|suspicious)/i)?.[1] || null,
      bodyLeak: document.body.innerText.slice(0, 800),
    };
  });

  console.log('verdict:', data.verdict);

  const ok = /normal|human/i.test(data.verdict || '');
  if (closeBrowser) await browser.close();
  return [{
    name: 'browserscan bot',
    pass: ok,
    detail: data.verdict || '(no verdict parsed)',
  }];
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const rows = await run();
  summary(rows);
}
