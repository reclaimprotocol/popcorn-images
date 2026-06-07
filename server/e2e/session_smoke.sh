#!/usr/bin/env bash
# Ad-hoc smoke test for the in-image browser-events worker (/session/*).
# Drives start -> navigate -> type -> click -> screenshot -> claim -> close
# against an ALREADY-RUNNING popcorn image.
#
# Usage:
#   BASE_URL=http://127.0.0.1:10001 ./session_smoke.sh
#
# Requires: curl, and (optionally) jq for pretty output.
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:10001}"
PAGE='data:text/html,<html><body><input id="u"><button id="b">go</button></body></html>'

pp() { if command -v jq >/dev/null 2>&1; then jq .; else cat; fi; }
post() { curl -fsS -X POST "$BASE_URL$1" -H 'Content-Type: application/json' -d "$2"; }
get()  { curl -fsS "$BASE_URL$1"; }

echo "==> base: $BASE_URL"

echo "==> POST /session/start"
post /session/start '{"provider_config":{"providerId":"smoke","loginUrl":"about:blank","injectionType":"NONE"}}' | pp

echo "==> GET /session/events (3s sample)"
curl -fsS -N --max-time 3 "$BASE_URL/session/events" || true
echo

echo "==> POST /session/action navigate"
post /session/action "$(printf '{"type":"navigate","payload":{"url":%s,"wait_until":"domcontentloaded"}}' "$(printf '%s' "$PAGE" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')")" | pp

echo "==> POST /session/action type"
post /session/action '{"type":"type","payload":{"selector":"#u","text":"hello","delay":0}}' | pp

echo "==> POST /session/action click"
post /session/action '{"type":"click","payload":{"selector":"#b"}}' | pp

echo "==> POST /session/action screenshot (success requires ENABLE_SCREENSHOTS=true in the image)"
post /session/action '{"type":"screenshot"}' | pp

echo "==> GET /session/claim"
get /session/claim | pp

echo "==> POST /session/close"
post /session/close '{}' | pp

echo "==> done"
