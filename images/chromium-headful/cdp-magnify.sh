#!/usr/bin/env bash
# Apply mobile-viewport emulation to the running chromium via CDP.
#
# Ports the CDP sequence from `applyViewportEmulation` in
# https://github.com/reclaimprotocol/popcorn-images/commit/bacfdb06 —
# the commit that originally introduced the magnify feature. Once the
# remote layout is mobile-sized, the popcorn client auto-applies its CSS
# crop based on the layout-viewport size it sees in the focus push, so the
# top-left screenW × screenH of the 1920×1080 framebuffer fills the screen.
#
# Key choices:
#   * deviceScaleFactor = 1 (DPR>1 makes chromium render at 2× and overflow
#     the stream, then clip — kills the crop)
#   * height is capped to the physical 1080 so the emulated viewport fits in
#     the framebuffer
#   * setUserAgentOverride: USER_AGENT is the UA chromium will report to
#     the loaded page. Defaults to iPhone Safari — overridable via env.
#     Note: this only changes navigator.userAgent on the REMOTE chromium.
#     It does NOT match the remote's TLS fingerprint to a mobile device
#     (that's still Linux chromium). Use a UA matching real devices when
#     bot detection compares the two; pick any plausible UA otherwise.
#
# Usage:
#   ./cdp-magnify.sh                     # default 390x844 with iPhone UA
#   ./cdp-magnify.sh 390 844
#   ./cdp-magnify.sh 390x844
#   ./cdp-magnify.sh 768x1024
#   USER_AGENT='...' ./cdp-magnify.sh 412x915     # override UA
#   ./cdp-magnify.sh reset                       # back to native 1920x1080
#
# Talks to the unfiltered internal devtools router on :9226. Make sure
# `-p 9226:9226` is in your `docker run` (run-docker.sh already exports it).
set -e -o pipefail

PHYSICAL_WIDTH=1920
PHYSICAL_HEIGHT=1080

WIDTH=390
HEIGHT=844
MODE=fit
CDP_HOST="${CDP_HOST:-localhost:9226}"

if [[ "$1" == "reset" ]]; then
  MODE=reset
elif [[ "$1" == *x* && "$1" != -* ]]; then
  WIDTH="${1%x*}"
  HEIGHT="${1#*x}"
  CDP_HOST="${2:-$CDP_HOST}"
elif [[ -n "$1" ]]; then
  WIDTH="$1"
  HEIGHT="${2:-844}"
  CDP_HOST="${3:-$CDP_HOST}"
fi

# Default UA: iPhone Safari (mobile-friendly). Override via USER_AGENT env.
DEFAULT_UA='Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1'
USER_AGENT="${USER_AGENT:-$DEFAULT_UA}"

for cmd in curl jq python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing: $cmd" >&2; exit 1
  fi
done

PAGE_TARGET=$(curl -fsS "http://${CDP_HOST}/json" \
  | jq -r '.[] | select(.type == "page") | select((.url // "") | startswith("devtools://") | not) | .id' \
  | head -n1)
if [[ -z "$PAGE_TARGET" ]]; then
  echo "no page target at http://${CDP_HOST}/json" >&2; exit 1
fi

BROWSER_WS=$(curl -fsS "http://${CDP_HOST}/json/version" | jq -r '.webSocketDebuggerUrl // empty')
[[ -z "$BROWSER_WS" ]] && BROWSER_WS="ws://${CDP_HOST}/devtools/browser"

echo "==> $BROWSER_WS"
if [[ "$MODE" == "reset" ]]; then
  echo "    mode=reset  target=$PAGE_TARGET"
else
  echo "    mode=fit  ${WIDTH}x${HEIGHT}  target=$PAGE_TARGET"
fi

exec python3 - "$BROWSER_WS" "$PAGE_TARGET" "$WIDTH" "$HEIGHT" "$MODE" "$USER_AGENT" "$PHYSICAL_WIDTH" "$PHYSICAL_HEIGHT" <<'PY'
import json, sys
try:
    from websockets.sync.client import connect
except ImportError:
    sys.stderr.write("missing python websockets: pip3 install websockets\n"); sys.exit(1)

ws_url, target_id, w_raw, h_raw, mode, ua, phys_w_raw, phys_h_raw = sys.argv[1:]
phys_w, phys_h = int(phys_w_raw), int(phys_h_raw)

def send(ws, msg_id, method, session_id=None, **params):
    payload = {"id": msg_id, "method": method, "params": params}
    if session_id:
        payload["sessionId"] = session_id
    ws.send(json.dumps(payload))

def recv_id(ws, want_id):
    while True:
        msg = json.loads(ws.recv())
        if msg.get("id") == want_id:
            return msg

with connect(ws_url, max_size=None) as ws:
    send(ws, 1, "Target.attachToTarget", targetId=target_id, flatten=True)
    res = recv_id(ws, 1)
    if "error" in res:
        sys.exit(f"attach failed: {res['error']}")
    session_id = res["result"]["sessionId"]

    # Persistent CSS that survives navigation. Page.addScriptToEvaluateOn-
    # NewDocument re-installs the cursor:none style on every fresh page
    # load so the cursor stays hidden across SPAs and route changes. The
    # immediate Runtime.evaluate handles the page that's already loaded.
    cursor_hide_js = "(()=>{var s=document.createElement('style');s.id='__popcorn_cursor_hide__';s.textContent='*,*::before,*::after{cursor:none!important;}';(document.head||document.documentElement).appendChild(s);})()"
    cursor_show_js = "(()=>{var s=document.getElementById('__popcorn_cursor_hide__');if(s)s.remove();})()"

    if mode == "reset":
        cmds = [
            ("Emulation.clearDeviceMetricsOverride", {}),
            ("Emulation.setTouchEmulationEnabled", {"enabled": False, "maxTouchPoints": 0}),
            ("Emulation.setUserAgentOverride", {"userAgent": ua}),
            # Remove the persistent cursor-hide script (idempotent — no
            # error if it was never installed). The Runtime.evaluate
            # cleanup tag matches what we installed below.
            ("Page.removeScriptToEvaluateOnNewDocument", {"identifier": "__popcorn_cursor_hide__"}),
            ("Runtime.evaluate", {"expression": cursor_show_js}),
        ]
    else:
        w, h = int(w_raw), min(int(h_raw), phys_h)
        # setDeviceMetricsOverride alone is enough — screenWidth/screenHeight
        # already constrain the rendered viewport. setVisibleSize is
        # deprecated in modern chromium and the client-side CSS crop
        # (auto-applied when the pushed viewportWidth shrinks below the
        # stream resolution) handles the framebuffer-vs-viewport gap.
        cmds = [
            ("Emulation.setDeviceMetricsOverride", {
                "width": w, "height": h,
                "deviceScaleFactor": 1,   # DPR>1 overflows the 1920x1080 stream
                "mobile": True,
                "screenWidth": w, "screenHeight": h,
            }),
            ("Emulation.setTouchEmulationEnabled", {"enabled": True, "maxTouchPoints": 5}),
            ("Emulation.setUserAgentOverride", {"userAgent": ua}),
            # Page.enable required before addScriptToEvaluateOnNewDocument.
            ("Page.enable", {}),
            ("Page.addScriptToEvaluateOnNewDocument", {
                "source": cursor_hide_js,
            }),
            # Apply to the currently-loaded page too.
            ("Runtime.evaluate", {"expression": cursor_hide_js}),
        ]

    next_id = 2
    for method, params in cmds:
        send(ws, next_id, method, session_id=session_id, **params)
        res = recv_id(ws, next_id)
        if "error" in res:
            print(f"{method:48s} error: {res['error']}", file=sys.stderr)
        else:
            print(f"{method:48s} ok")
        next_id += 1
PY
