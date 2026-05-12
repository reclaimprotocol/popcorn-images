# Stealth surface — what's in the image and why

This image is built to pass aggressive anti-bot detection (Akamai BMP /
"airline-tier", Cloudflare Bot Management, reCAPTCHA Enterprise, Kasada,
PerimeterX) so the browser can be used as a **public remote browser** —
real human users drive it via WebRTC streaming. The threat model here is
specific: we are *not* trying to evade detection from a known operator;
we *are* trying to look like a regular Windows Chrome session to bot
classifiers that score every visitor.

This doc is the single source of truth for what stealth measures are in
place, what they cost, and what's deliberately *not* there.

If you're trying to verify stealth quickly: run the suite under
[`stealth-tests/`](../stealth-tests/README.md). It exercises everything
described below against the running image.

---

## Layers

The stealth surface is layered. A failure at any layer typically means
the bot classifier wins; success at every layer is what gets you `_abck`
verdict `~0~` from Akamai.

### 1. TLS / HTTP-2 (network layer)

**Goal**: the network signature matches real Chrome on Windows, not
headless-Chrome on Linux.

What we ship:

- **CloakBrowser binary** (pinned by digest in the Dockerfile). It's a
  chromium fork with C++-level patches that produce a real-Chrome JA3,
  JA4, peetprint, and Akamai HTTP/2 fingerprint. GREASE values are
  present, cipher list order matches stock Chrome, the HTTP/2 SETTINGS
  frame and pseudo-header order (`m,a,s,p`) are canonical.
- Verified by `stealth-tests/probes/tls-peet.mjs` against
  [tls.peet.ws](https://tls.peet.ws). The pass criterion is that JA4
  contains the cipher-list hash `_8daaf6152771_` — the exact extension
  count (1516 vs 1517) drifts with flags, but the cipher signature is
  the Chrome tell.

What we do **not** do:

- We don't try to spoof a different Chrome version's fingerprint.
  CloakBrowser's binary already presents whatever real Chrome version
  matches the image's Chromium build.
- We don't run a TLS proxy or MITM. The fingerprint comes from the
  in-container chromium itself.

### 2. Static JS fingerprint (browser surface)

**Goal**: `navigator.*`, `screen.*`, `WebGL`, `AudioContext`, fonts, and
plugins all read like a real Windows Chrome session.

What we ship:

- `--fingerprint=<seed>`: CloakBrowser's per-instance seed; persisted in
  `/home/kernel/user-data/.cloak-fingerprint-seed` so the same pod
  presents the same identity across restarts (bot classifiers correlate
  fingerprint drift on the same cookie jar — flipping is itself a tell).
  The seed is generated from `/dev/urandom`, not bash `$RANDOM` (15-bit,
  collides at fleet scale). The seed deterministically maps to every
  derived dimension including the spoofed `UNMASKED_RENDERER_WEBGL`
  string — **a non-trivial fraction of seeds map to flagship discrete
  GPUs ("NVIDIA GeForce RTX 4070 Ti" etc.), which under this image's
  SwiftShader rasterization is a deterministic Akamai tell** (the
  pixel-readback hash matches SwiftShader, never a real NVIDIA driver).
  Override with `CLOAK_FINGERPRINT_SEED=<n>` to pin a known-good seed
  that resolves to an integrated Intel GPU; discover candidates with
  `scripts/probe-fingerprint.sh`.
- `--fingerprint-platform=windows`: spoofs `navigator.platform`,
  `userAgentData.platform`, and the Sec-CH-UA-Platform header to
  Windows from a Linux container.
- `--fingerprint-timezone=<tz>` and `--fingerprint-locale=<loc>`: aligned
  with the proxy egress IP via ipapi.co lookup at boot
  (`CLOAK_GEOIP=true`, default on). Persisted alongside the seed so
  they don't drift across restarts. The OS-level `TZ` env and
  `/etc/localtime` are also realigned so `Date` arithmetic matches Intl
  results — a one-line mismatch Akamai watches for.
- `--fingerprint-webrtc-ip=auto`: WebRTC STUN candidate addresses are
  resolved to the proxy exit IP, not the container's real IP.
- `--use-fake-device-for-media-stream`: `enumerateDevices()` reports
  three synthetic entries (`audioinput`, `videoinput`, `audiooutput`)
  matching the shape real Chrome exposes pre-permission. Without this,
  the empty list is a deterministic headless tell.
- **611 fonts installed** — including the four CloakBrowser specifies
  for canvas/glyph hashing (`fonts-noto-color-emoji`, `fonts-unifont`,
  `fonts-ipafont-gothic`, `fonts-wqy-zenhei`, `fonts-tlwg-loma-otf`)
  plus the standard Linux desktop set. Kasada and Akamai render emoji
  and CJK glyphs to a hidden canvas and hash the pixel output; missing
  fonts produce tofu boxes that hash differently from a real OS.
- **Page-world overrides** in `extensions/proxy/page.js` for the kiosk
  geometry leak (`outerHeight` < `innerHeight` is physically impossible
  on real Chrome and a deterministic Akamai signal under Xdummy).
- Stealth banner at boot (`[stealth] ...` lines in
  `docker logs chromium-headful-test`) surfaces what's active.

What we do **not** do:

- ❌ No `navigator.webdriver = false` JS injection. CloakBrowser handles
  this at the chromium C++ level; JS overlay would *introduce* a tell.
- ❌ No `Object.defineProperty` wrappers around navigator props. Those
  leave detectable proxy/toString traces.
- ❌ No `chrome.runtime`/`chrome.app` faking. CloakBrowser provides
  these natively because it *is* chromium.

### 3. Behavioral signature (human-in-the-loop layer)

**Goal**: the page reads mouse movements with realistic timing jitter,
scrolls with momentum, key cadence with human variance. Akamai BMP's
sensor explicitly scores the *absence* of these events as bot-like.

What we ship:

- For the **public remote browser** use case (the primary one): the
  end user drives the browser via Neko / WebRTC streaming. Their real
  mouse moves arrive through `xf86-input-neko` and produce genuine
  `mousemove`, `mousedown`, `click`, `wheel`, `keydown`, `keyup` events
  with real-human timing. **This is the strongest possible behavioral
  signature — better than any synthetic humanizer**, because it
  literally is a human.
- For **automated workflows** (consumer-side Playwright/Patchright):
  the consumer is expected to call CloakBrowser's
  `patchBrowser(browser, resolveConfig('default'))` from
  `cloakbrowser/human` to enable smooth mouse curves and click jitter.
  This is surfaced in the boot banner as a reminder.

Empirical result: synthetic humanize (multi-step mouse moves + smooth
scroll + 1500 ms dwell) flips Delta's `_abck` from `~-1~` to `~0~`. Real
human behavior is strictly stronger.

### 4. Network egress (IP layer)

**Goal**: the source IP doesn't trigger per-property IP-reputation
blocks.

What we ship:

- BrightData residential proxy plumbing via `--proxy-server=...` at
  launch + CDP `Fetch.authRequired` handler on the consumer side (the
  MV3 webRequest path doesn't fire on proxy CONNECT challenges —
  chromium bug 40274579). Sticky sessions configured via the username
  template (`-country-jp-session-<id>` etc.).
- `__pcn` page-world API in `extensions/proxy/` for runtime proxy
  changes without restarting chromium.

What we do **not** do:

- We don't ship a proxy pool, rotation logic, or per-target failover.
  That belongs in the consumer (the WebSDK), not in the image.

### 5. Profile / cookie warming (long-tail bot signals)

**Goal**: bot classifiers correlate "fresh" cookie jars (no HSTS state,
no TLS session resumption cache, no localStorage on common origins)
with bots. Warmed profiles look like long-running real users.

What we ship:

- `CLOAK_PROFILE_SEED` env: at boot, if a tarball is provided and the
  user-data dir is empty, `wrapper.sh` extracts it. The bundle should
  be produced from a successful supervised session via
  `scripts/seed-profile.sh`, which excludes ephemeral caches and
  includes `Default/Cache` (warms TLS session resumption + Akamai
  per-origin sensor history).
- The bundle pattern is the canonical way to ship "pre-authenticated"
  worker pods. See `wrapper.sh` near `CLOAK_PROFILE_SEED` for details.

---

## What works, what doesn't (current scoreboard)

Run `stealth-tests/run.mjs` to refresh this. Last measured results on
the current image:

| Probe                   | Status  | Notes                                                                 |
| ----------------------- | ------- | --------------------------------------------------------------------- |
| TLS / HTTP-2 (peet.ws)  | ✅ PASS | JA4 Chrome-canonical, Akamai H2 fingerprint matches Chrome 120+       |
| bot.sannysoft.com       | ✅ PASS | 31/31 automation checks green                                         |
| Akamai BMP — Delta      | ✅ PASS | `_abck` token `~0~` after behavioral signals                          |
| Akamai BMP — Finnair    | ✅ PASS | `_abck` token `~0~`                                                   |
| Akamai BMP — Hilton     | ✅ PASS | `_abck` token `~0~`                                                   |
| Akamai BMP — ANA        | ❌ FAIL | `_abck` token `~-1~`. Persists across IPs (Indian res + BrightData JP) and across fonts/humanize. Almost certainly per-property IP-reputation against BrightData ranges; **not a fingerprint issue**. Fix is a different proxy provider for ANA specifically, not an image change. |
| Cloudflare              | ✅ PASS | No challenge on nopecha demo or cloudflare.com                        |
| reCAPTCHA v3            | ~ varies | 0.7-0.9 on residential IPs with cookie warming; 0.3-0.6 on cold-start without google.com history |
| CreepJS                 | ~ 70-85 | Healthy; specific bot tells (webdriver, headless) absent              |

---

## Deliberate non-decisions

Things we explicitly chose *not* to do, with reasons. Re-read these
before reopening any of them.

### ❌ Don't pivot to Camoufox

- TLS layer is already clean (peet.ws confirms real Chrome 146 Windows).
- 3 of 4 known target sites pass on the current stack.
- ANA's `~-1~` persists across IPs and humanize — pattern matches
  IP-reputation, not browser-engine fingerprint. Camoufox wouldn't move
  it.
- Pivot cost: rewriting the `__pcn` proxy extension as a WebExtension,
  bridging Neko's chromium-specific compositor flags, retraining the
  chromedriver test infra. Multi-week rebuild for an unproven hypothesis.
- Revisit only if (a) CreepJS reveals a Chrome-headless tell we can't
  fix in CloakBrowser, or (b) multiple high-value targets reject the
  current stack on signals we can prove are JS-engine-specific.

### ❌ Don't add `--renderer-process-limit`

- Considered as a 3 GiB-pod OOM guard. Reverted because it weakens
  Spectre/Meltdown site-isolation guarantees and introduces tab-switch
  jitter on heavy SPAs.
- Mitigation if OOM becomes real: bump pod RAM to 4 GiB rather than
  re-introducing the cap.

### ❌ Don't bundle a passkey-provider extension (Bitwarden, 1Password)

- Single-tenant logic doesn't fit a multi-tenant public browser.
- A shared profile + a personal vault is a credential-bleed risk.
- See [`docs/AUTH-PATHS.md`](./AUTH-PATHS.md).

### ❌ Don't ship a synthetic-behavior daemon

- Real user input via Neko is strictly better than any synthetic
  humanizer. For automated workflows, the consumer applies
  `cloakbrowser/human` patches — that's their layer, not the image's.

### ❌ Don't auto-grant getUserMedia (`--use-fake-ui-for-media-stream`)

- The fake-device flag is on (closes the empty-mediaDevices tell), but
  the UI auto-grant is off — on a public/multi-tenant browser, silently
  approving microphone/camera access for any visiting site is an
  unacceptable security regression. The permission prompt stays; only
  the device-list shape changes.

### ❌ Don't enable `--mute-audio`

- Removed in an earlier cleanup. Akamai watches for `AudioContext`
  output shape; with mute on, the audio fingerprint hash is
  deterministic-but-suspicious. PulseAudio is also disabled by default
  (`ENABLE_PULSEAUDIO=false`), so there's no actual output device —
  CloakBrowser handles the AudioContext spoof in software.

---

## Verifying after a change

Anything that touches `wrapper.sh`, `Dockerfile`, the `extensions/`
tree, or the CloakBrowser version pin should be followed by:

1. Rebuild: `./build-docker.sh`
2. Launch: `./run-docker.sh` (or the equivalent `docker run` with
   `RUN_AS_ROOT=true` on ARM hosts)
3. Boot-banner check: `docker logs chromium-headful-test | grep stealth`
   — every flag described above should be visible.
4. Suite: `stealth-tests/run.mjs` — see
   [`stealth-tests/README.md`](../stealth-tests/README.md).
5. Spot-check the live browser (Neko or screenshot) on Delta + sannysoft
   + creepjs for visual confirmation.

A regression on TLS, sannysoft, or Akamai (Delta/Finnair/Hilton) is a
real signal — investigate before merging. A regression on ANA is
expected and not blocking.
