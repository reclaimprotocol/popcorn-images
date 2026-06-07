#!/usr/bin/env bash
# Navigate the running chromium container to a URL via CDP.
#
# Usage:   ./cdp-navigate.sh <url> [host:port]
# Example: ./cdp-navigate.sh https://example.com
#          ./cdp-navigate.sh https://kaggle.com/account/login 127.0.0.1:9226
#
# Defaults to localhost:9226 — the internal *unfiltered* devtools router. The
# public proxy on :9222 has a Page.navigate-rejecting allowlist; the internal
# one is open. The devtools proxy ignores the WS request path and always
# delivers a browser-level connection, so we Target.attachToTarget the page
# (flatten=true) and dispatch Page.navigate against that session.
set -e -o pipefail

URL="${1:?usage: $0 <url> [host:port]}"
CDP_HOST="${2:-localhost:9226}"

for cmd in curl jq python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing: $cmd" >&2
    exit 1
  fi
done

PAGE_TARGET=$(curl -fsS "http://${CDP_HOST}/json" \
  | jq -r '.[] | select(.type == "page") | select((.url // "") | startswith("devtools://") | not) | .id' \
  | head -n1)
if [[ -z "$PAGE_TARGET" ]]; then
  echo "no page target at http://${CDP_HOST}/json" >&2
  exit 1
fi

BROWSER_WS=$(curl -fsS "http://${CDP_HOST}/json/version" | jq -r '.webSocketDebuggerUrl // empty')
if [[ -z "$BROWSER_WS" ]]; then
  BROWSER_WS="ws://${CDP_HOST}/devtools/browser"
fi

echo "==> $BROWSER_WS (target=$PAGE_TARGET) -> $URL"

exec python3 - "$BROWSER_WS" "$PAGE_TARGET" "$URL" <<'PY'
import json, sys
try:
    from websockets.sync.client import connect
except ImportError:
    sys.stderr.write("missing python websockets: pip3 install websockets\n"); sys.exit(1)

ws_url, target_id, url = sys.argv[1], sys.argv[2], sys.argv[3]
with connect(ws_url, max_size=None) as ws:
    ws.send(json.dumps({
        "id": 1,
        "method": "Target.attachToTarget",
        "params": {"targetId": target_id, "flatten": True},
    }))
    session_id = None
    while session_id is None:
        msg = json.loads(ws.recv())
        if msg.get("id") == 1:
            if "error" in msg:
                sys.exit(f"attach failed: {msg['error']}")
            session_id = msg["result"]["sessionId"]
    ws.send(json.dumps({
        "id": 2,
        "method": "Page.navigate",
        "sessionId": session_id,
        "params": {"url": url},
    }))
    while True:
        msg = json.loads(ws.recv())
        if msg.get("id") == 2:
            print(json.dumps(msg))
            break
PY
