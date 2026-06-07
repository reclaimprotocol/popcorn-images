#!/usr/bin/env bash
#
# Drive a session from a real provider config against a running popcorn image.
#
# Opens the SSE event stream first (so nothing is missed), starts a session from
# the given provider config, prints events live (network-request/response,
# page-loaded, claim, ...), and on exit prints the accumulated proofs and closes
# the session.
#
# Usage:
#   BASE_URL=http://127.0.0.1:444 ./run_provider_config.sh config.json
#
# The config file may be:
#   - the raw "provider config fetched" response (has .providerConfig.providerConfig)
#   - an already-wrapped {"provider_config": {...}}
#   - a bare provider-config object ({loginUrl, customInjection, requestData, ...})
#
# Env:
#   BASE_URL         API base (default http://127.0.0.1:444 — the run-docker.sh port)
#   EVENTS_SECONDS   how long to watch the stream (default 300; Ctrl-C stops early)
#
# Requires: curl, jq.
set -uo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:444}"
CONFIG="${1:-config.json}"
EVENTS_SECONDS="${EVENTS_SECONDS:-300}"

command -v jq   >/dev/null 2>&1 || { echo "error: jq is required"; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "error: curl is required"; exit 1; }
[[ -f "$CONFIG" ]] || { echo "error: config file not found: $CONFIG (pass its path as arg 1)"; exit 1; }

# Build the /session/start body, accepting the three shapes described above.
if jq -e '.providerConfig.providerConfig' "$CONFIG" >/dev/null 2>&1; then
  START=$(jq -c '{provider_config: (.providerConfig.providerConfig + {providerId: (.providerConfig.providerId // "provider")})}' "$CONFIG")
elif jq -e '.provider_config' "$CONFIG" >/dev/null 2>&1; then
  START=$(jq -c '{provider_config: .provider_config}' "$CONFIG")
else
  START=$(jq -c '{provider_config: .}' "$CONFIG")
fi

PROVIDER_ID=$(printf '%s' "$START" | jq -r '.provider_config.providerId // "?"')
LOGIN_URL=$(printf '%s' "$START" | jq -r '.provider_config.loginUrl // "?"')
MATCHERS=$(printf '%s' "$START" | jq -r '(.provider_config.requestData // []) | length')

finish() {
  echo
  echo "==> GET /session/claim"
  curl -fsS "$BASE_URL/session/claim" 2>/dev/null | jq . || echo "(no active session)"
  echo "==> POST /session/close"
  curl -fsS -X POST "$BASE_URL/session/close" -H 'Content-Type: application/json' -d '{}' 2>/dev/null | jq -c . || true
}
trap finish EXIT

echo "==> base:       $BASE_URL"
echo "==> provider:   $PROVIDER_ID   loginUrl: $LOGIN_URL   requestData matchers: $MATCHERS"

# Subscribe to events BEFORE starting, so early events aren't missed.
(
  curl -N --max-time "$EVENTS_SECONDS" "$BASE_URL/session/events" 2>/dev/null \
  | while IFS= read -r line; do
      [[ "$line" == data:* ]] || continue
      payload="${line#data: }"
      printf '[event] '
      printf '%s\n' "$payload" | jq -c . 2>/dev/null || printf '%s\n' "$payload"
    done
) &
STREAM_PID=$!
sleep 1

echo "==> POST /session/start"
if ! printf '%s' "$START" | curl -fsS -X POST "$BASE_URL/session/start" \
      -H 'Content-Type: application/json' -d @- | jq .; then
  echo "error: /session/start failed (a session may already be active — re-run to close it, or check the image logs)"
  exit 1
fi

echo "==> watching /session/events for up to ${EVENTS_SECONDS}s (Ctrl-C to stop; claims print on exit)"
wait "$STREAM_PID" 2>/dev/null || true
