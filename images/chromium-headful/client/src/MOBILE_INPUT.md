# Mobile input layer (popcorn ↔ portal)

This document describes the touch / keyboard / IME / viewport plumbing that
lives in `src/components/video.vue` and the `postMessage` contract popcorn
exposes for an embedding portal (web-sdk, etc.).

The intent is that an embedding portal **disables its own** touch-and-keyboard
layer on popcorn-served pages — popcorn handles everything internally now —
and only renders portal-level UI (custom select dropdowns, branded keyboard
chrome, viewport-driven layout) in response to the events below.

## What's inside the popcorn page

| Concern | Where | Notes |
|---|---|---|
| Touch overlay (tap / swipe / long-press / pinch detect) | `onTouchHandler` in `video.vue` | State machine in `touchMode`; 8 px move threshold; 500 ms long-press |
| Mobile IME proxy | hidden `<input class="mobile-proxy-input">` | `opacity:0.01`, `font-size:16px` (iOS no-zoom), `inputmode="text"`, all autocorrect attrs off |
| Per-platform IME logic | `onProxyBeforeInput` / `onProxyInput` / `onProxyKeyDown` / `onProxyBlur` / `onCompositionEnd` | Android uses value-comparison (Samsung Keyboard treats every word as composition); iOS uses beforeinput |
| SwiftKey auto-space stripper | `filterAutoSpace` | Removes the space SwiftKey inserts after `!./,?:;'"()]}` |
| Indic IME deferred backspace | `onProxyKeyDown` `'Unidentified'` branch | 80 ms grace; cancelled by any beforeinput/input that follows |
| Keyboard show/hide detect | `handleViewportResize` via `window.visualViewport` | Requires explicit "shrunk first" observation before treating a grow event as dismissal |
| iOS gesture bridge | `focusProxyInputForIOS` | Temp readonly input → real proxy (double-RAF). iframe-embedded path uses direct focus instead |
| Input transport | `Input.insertText` / `Input.dispatchKeyEvent` via page-scoped CDP WebSocket | `connectCDP()` opens `ws://host:9222/devtools/page/<id>` on mount |

## postMessage protocol

All messages are JSON objects with a `type` field. Origin is `*` since both
sides are typically same-organisation but cross-origin (popcorn vs portal).

### Outbound (popcorn → portal)

#### `POPCORN_INPUT_READY`

```json
{ "type": "POPCORN_INPUT_READY" }
```

Sent once after the touch/keyboard layer is mounted and the CDP socket has
opened. The portal **must** wait for this before disabling its own touch
layer — otherwise tap-to-focus races in the gap between popcorn's iframe
loading and its keyboard layer initialising.

#### `POPCORN_VIEWPORT`

```json
{
  "type": "POPCORN_VIEWPORT",
  "visibleHeight": 540,
  "occludedBottom": 304
}
```

Sent whenever `window.visualViewport` resizes (typically: soft keyboard
appears, soft keyboard dismisses). `occludedBottom` is how many pixels of
the viewport are hidden by the keyboard (zero when keyboard is down).
Portals can use this to position their own chrome above the keyboard.

#### `POPCORN_SHOW_SELECT`

```json
{
  "type": "POPCORN_SHOW_SELECT",
  "rect":     { "x": 100, "y": 200, "width": 240, "height": 36 },
  "multiple": false,
  "options":  [
    { "value": "us", "text": "United States", "selected": true,  "disabled": false, "groupLabel": null },
    { "value": "ca", "text": "Canada",        "selected": false, "disabled": false, "groupLabel": null }
  ]
}
```

Sent when the user taps a `<select>` on the remote page. The portal renders
its own dropdown UI overlaid against `rect` and `postMessage`s back. Popcorn
suppresses Chromium's native dropdown (which would otherwise block
`page.evaluate` calls).

Implementation: server-side `activeElementExpression` extracts `selectInfo`
(options + rect + multiple flag) whenever the focused element is a `<select>`;
client-side `maybeAnnounceSelect()` runs ~60 ms after each tap, checks the
focused element, and emits this message if a select is in focus.

### Inbound (portal → popcorn)

#### `PORTAL_SET_SELECT_VALUE`

```json
{ "type": "PORTAL_SET_SELECT_VALUE", "values": ["us"] }
```

Apply the user's selection to the remote `<select>` element. `values` is an
array of `value` strings; for single-select use a one-element array.

#### `PORTAL_CLOSE_SELECT`

```json
{ "type": "PORTAL_CLOSE_SELECT" }
```

User dismissed the dropdown without picking; restore focus to whatever was
active before.

## CDP transport choice

The Vue client connects directly to chromium over WebSocket at
`ws://hostname:9222/devtools/page/<id>` for **mechanical input** —
`Input.insertText`, `Input.dispatchKeyEvent`, `Input.dispatchMouseEvent`,
`Emulation.setDeviceMetricsOverride`, etc. These are all on the
kernel-images-api allowlist (`server/lib/devtoolsproxy/proxy.go:563`).

`Runtime.evaluate` is **deliberately NOT in the allowlist**: it would let any
caller reachable on `:9222` execute arbitrary JS in the chromium page. When
the Vue client needs to run scripted DOM mutations (e.g. applying the user's
dropdown selection back to the focused `<select>`), it POSTs to a typed
server-side endpoint — `/cdp/set-select-value`, `/cdp/active-element`,
`/cdp/emulate-device` — which uses the unfiltered upstream CDP socket
internally. Each new use case for eval gets its own narrow endpoint instead
of a blanket Runtime.evaluate primitive.

This keeps the security boundary even when the Vue page is served from the
same container as the proxy.

## Integration checklist for a portal

1. Detect popcorn-served pages via your existing `isKernelUrl()` check.
2. Gate your touch overlay / proxy input OFF when popcorn is detected.
3. Listen for `POPCORN_INPUT_READY`; only then assume popcorn owns input.
4. Listen for `POPCORN_VIEWPORT` to mirror keyboard state in your own UI.
5. Listen for `POPCORN_SHOW_SELECT`; render dropdown; reply with
   `PORTAL_SET_SELECT_VALUE` or `PORTAL_CLOSE_SELECT`.

## Status

- ✅ Phase 1 — Proxy input, per-platform IME, visualViewport detect, iOS bridge
- ✅ Phase 2a — SwiftKey auto-space, Indic `Unidentified` deferred backspace, voice/glide batching
- ✅ Phase 2b — Android auto-focus poller (`/cdp/active-element` now returns `focusKey`, `readonly`, `disabled`)
- ✅ Phase 3  — `POPCORN_SHOW_SELECT` + `PORTAL_SET_SELECT_VALUE` + `PORTAL_CLOSE_SELECT`

## Manual test plan

Run the matrix below against `https://www.kaggle.com/account/login` (any login
form with a text input + a `<select>` would work as a surrogate).

| Device + Browser + IME | Tap input | Type word | Backspace | Autocomplete tap | Swipe-type | `<select>` |
|---|---|---|---|---|---|---|
| iPhone Safari (standalone) | Keyboard pops | letters appear | works | inserts word | n/a | (Phase 3) |
| iPhone Safari (iframe-embedded) | Keyboard pops | letters appear | works | inserts word | n/a | (Phase 3) |
| Android Chrome + Gboard | Keyboard pops | letters appear in order | works | inserts word | inserts word | (Phase 3) |
| Android Chrome + SwiftKey | Keyboard pops | letters appear; **no auto-space after `!.,?`** | works | inserts word | inserts word | (Phase 3) |
| Samsung Internet + Samsung Keyboard | Keyboard pops | every word inserted via value-comparison | works | inserts word | inserts word | (Phase 3) |
| Firefox Android | Keyboard pops | letters appear | works | inserts word | n/a | (Phase 3) |
| Chinese — Sogou / Baidu / Iflytek | Keyboard pops | pinyin → selected character inserted | works | n/a | n/a | (Phase 3) |
| Korean — Naver SmartBoard / Daum | Keyboard pops | hangul composes correctly | works | n/a | n/a | (Phase 3) |
| Bengali — Ridmik | Keyboard pops | letters appear; **`Unidentified` backspace works** | works | n/a | n/a | (Phase 3) |

For each row, additionally verify:
- Soft keyboard pops within the first tap (no double-tap required).
- Focused field is lifted above the keyboard (not occluded).
- Dismissing the keyboard via system back / down-chevron clears the lift.
- Tapping a non-input while the keyboard is up dismisses it.
