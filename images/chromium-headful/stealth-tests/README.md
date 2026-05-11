# Stealth-probe suite

Repeatable end-to-end checks for the chromium-headful image's anti-bot
surface. Run against a live container — these probes attach over CDP and
drive the in-container chromium directly, so they exercise the *real*
image, not a mock.

## Probes

| Probe         | Pass criterion                                         |
| ------------- | ------------------------------------------------------ |
| `tls`         | tls.peet.ws JA4 starts with `t13d1516h2_8daaf6152771_` |
| `akamai`      | `_abck` token == `0` on Delta/Finnair/Hilton/ANA       |
| `creepjs`     | CreepJS trust score ≥ 70%                              |
| `sannysoft`   | Zero failed checks on bot.sannysoft.com                |
| `cloudflare`  | chat.openai / discord / cloudflare.com served without managed-challenge interstitial |
| `turnstile`   | peet.ws non-interactive Turnstile auto-validates and emits a token within 25s |
| `recaptcha`   | reCAPTCHA v3 score ≥ 0.7 via antcpt.com                |
| `browserscan` | browserscan.net verdict == "Normal"/"Human"            |

## Run

Start the container the usual way (`run-docker.sh`), then in another
shell from the project root:

```bash
# All probes
docker exec chromium-headful-test bash -c \
  'mkdir -p /tmp/node_modules && \
   ln -sf /usr/local/lib/node_modules/playwright-core /tmp/node_modules/playwright-core && \
   cp -r /host/stealth-tests /tmp/stealth-tests && \
   cd /tmp && node stealth-tests/run.mjs'
```

Or, during development, copy the test dir in fresh each iteration. Note
`docker cp` MERGES into an existing target directory rather than
replacing it — repeated copies leave stale files behind. Always `rm -rf`
the in-container target first:

```bash
docker exec chromium-headful-test bash -c \
  'mkdir -p /tmp/node_modules && \
   ln -sf /usr/local/lib/node_modules/playwright-core /tmp/node_modules/playwright-core && \
   rm -rf /tmp/stealth-tests'

docker cp stealth-tests chromium-headful-test:/tmp/stealth-tests

docker exec chromium-headful-test bash -c \
  'cd /tmp && node stealth-tests/run.mjs'
```

### Subset of probes

```bash
docker exec chromium-headful-test bash -c \
  'cd /tmp && node stealth-tests/run.mjs tls akamai sannysoft'
```

### With BrightData proxy

Place credentials at `/tmp/proxy-creds.json` in the container:

```json
{ "username": "brd-customer-...-country-jp-session-XXX",
  "password": "..." }
```

Then:

```bash
docker exec chromium-headful-test bash -c \
  'cd /tmp && PROXY_AUTH=1 node stealth-tests/run.mjs akamai'
```

The probes themselves don't *set* the proxy — that's done at chromium
launch via `--proxy-server=...` in the flags file. `PROXY_AUTH=1` only
tells the probes to install a CDP `Fetch.authRequired` handler with the
credentials.

## Interpreting results

The summary block at the end prints one line per probe:

```
══ SUMMARY ═══════════════════════════════════════════
  ✓  TLS JA4          Chrome-canonical (t13d1516h2_…)
  ✓  Akamai delta.com   _abck token ~0~
  ✗  Akamai ana.co.jp   _abck token ~-1~
  !  CreepJS trust    65% trust
══════════════════════════════════════════════════════
```

`✓` pass, `✗` fail, `!` warning (something to investigate but not a
hard regression). The orchestrator exits 0 if every probe passes, 1 if
any fails outright, 2 if any probe threw.

## Known expected results on current image

- **tls / akamai (Delta/Finnair/Hilton) / cloudflare** — PASS
- **akamai (ANA)** — FAIL, IP-rep on BrightData ranges; not a fingerprint issue
- **creepjs / sannysoft / browserscan** — should pass; watch the diff over
  time, regressions here indicate a CloakBrowser-side surface change
- **recaptcha** — passes on residential IPs, can warn on first run when
  the cookie jar has no Google history (cold-start signal)

## When a probe regresses

1. Re-run the failing probe in isolation to confirm.
2. Check `docker logs chromium-headful-test | grep stealth` — every flag
   that's supposed to be on should appear in the boot banner.
3. Check `peet.ws` and `bot.sannysoft.com` manually in the live browser
   (Neko or via screenshot) — sometimes our scraping of the page DOM is
   what broke, not the underlying signal.
4. Bisect against the most recent image change (Dockerfile or wrapper.sh).
