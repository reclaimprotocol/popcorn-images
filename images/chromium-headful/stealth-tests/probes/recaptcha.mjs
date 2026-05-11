// reCAPTCHA v3 score probe.
//
// v3 is silent: there's no challenge UI, just a per-request score
// (0.0 = bot, 1.0 = human). The score is normally server-side only, so we
// use a public demo that surfaces it:
//   https://antcpt.com/score_detector/   ← Anti-CAPTCHA's score viewer
// which runs grecaptcha.execute() on a known site key and renders the
// numeric score in the DOM.
//
// Healthy real-Chrome target on a clean residential IP is 0.7-0.9. Below
// 0.3 means reCAPTCHA flagged us hard; 0.3-0.6 is borderline.

import { connect, humanize, section, summary } from '../utils.mjs';

export async function run({ closeBrowser = true } = {}) {
  const { browser, page } = await connect();
  section('reCAPTCHA v3 score');

  await page.goto('https://antcpt.com/score_detector/', { waitUntil: 'domcontentloaded', timeout: 30000 });
  await humanize(page, { rounds: 4, dwell: 2000 });
  await page.waitForTimeout(8000);

  // antcpt renders score in #recaptcha-score
  const data = await page.evaluate(() => {
    function txt(sel) { const el = document.querySelector(sel); return el ? el.innerText.trim() : null; }
    return {
      score: txt('#recaptcha-score') || document.body.innerText.match(/your score is[:\s]*([\d.]+)/i)?.[1] || null,
      raw: document.body.innerText.match(/[\d]\.[\d]+/g)?.slice(0, 3) || [],
    };
  });

  console.log('parsed score:', data.score);
  console.log('possible numeric candidates on page:', data.raw);

  const score = parseFloat(data.score);
  const ok = !isNaN(score) && score >= 0.7;
  const warn = !isNaN(score) && score >= 0.3 && score < 0.7;

  if (closeBrowser) await browser.close();
  return [{
    name: 'reCAPTCHA v3',
    pass: ok,
    warn,
    detail: isNaN(score) ? '(score not parsed)' : `score=${score}`,
  }];
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const rows = await run();
  summary(rows);
}
