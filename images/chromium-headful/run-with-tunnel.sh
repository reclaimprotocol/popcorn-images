#!/usr/bin/env bash
# One-shot launcher: container with WebRTC + Cloudflare Calls TURN, plus a
# cloudflared quick-tunnel that path-routes /cdp/* and /api/* to the
# kernel-images-api so the mobile client works through one HTTPS origin.
# Avoids the iceservers-newline footgun by minting credentials inside the
# script instead of pasting JSON at the prompt.
set -e -o pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

# Cloudflare Calls TURN — short-lived creds minted via API at startup.
TURN_KEY_ID="${TURN_KEY_ID:-ebd479e992e9beff26344e214843fef1}"
TURN_API_TOKEN="${TURN_API_TOKEN:-cac21320baa6b71fb59ec1d9822cfcd4119373e1533621b657916ca1bb58a656}"

CF_RESP=$(curl -fsS -X POST \
  "https://rtc.live.cloudflare.com/v1/turn/keys/${TURN_KEY_ID}/credentials/generate" \
  -H "Authorization: Bearer ${TURN_API_TOKEN}" \
  -H "Content-Type: application/json" \
  --data '{"ttl":1800}')

ICE_JSON=$(printf '%s' "$CF_RESP" | jq -c '[.iceServers]')

# Sanity: assert single-line, valid JSON before we hand it to docker.
# Use bash parameter expansion (BSD grep on macOS doesn't reliably match $'\n').
case "$ICE_JSON" in
  *$'\n'*|*$'\r'*)
    echo "BUG: ICE_JSON in the script has a literal newline — open this file and rejoin the line." >&2
    exit 1
    ;;
esac
if ! printf '%s' "$ICE_JSON" | jq empty 2>/dev/null; then
  echo "BUG: ICE_JSON isn't valid JSON." >&2
  exit 1
fi

docker rm -f chromium-headful-test 2>/dev/null || true

export NEKO_ICESERVERS="$ICE_JSON"
export WITH_KERNEL_IMAGES_API=true
export ENABLE_WEBRTC=true
export RUN_AS_ROOT=true

# Bring up cloudflared with path-based ingress so /cdp/* and /api/* route to
# the kernel-images-api (host:444) and everything else routes to neko (:8080).
# Without this, the mobile client falls back to http://<host>:9222/cdp/... and
# Safari blocks it as mixed content — keyboard never pops, keystrokes go
# nowhere.
TUNNEL_PID=""
TUNNEL_LOG="$SCRIPT_DIR/.tmp/cloudflared.log"
mkdir -p "$(dirname "$TUNNEL_LOG")"

if command -v cloudflared >/dev/null 2>&1; then
  : > "$TUNNEL_LOG"
  # `--url http://localhost:8080` is required even with a config file to
  # activate the free quick-tunnel mode. Ingress rules from the config still
  # take precedence for matching paths.
  cloudflared tunnel \
    --config "$SCRIPT_DIR/cloudflared.yml" \
    --url http://localhost:8080 \
    >>"$TUNNEL_LOG" 2>&1 &
  TUNNEL_PID=$!

  # Pull the assigned trycloudflare.com URL out of the log. cloudflared prints
  # it as `https://<slug>.trycloudflare.com` inside a banner.
  echo "==> cloudflared starting (pid $TUNNEL_PID)…"
  for _ in $(seq 1 40); do
    TUNNEL_URL=$(grep -Eo 'https://[a-z0-9-]+\.trycloudflare\.com' "$TUNNEL_LOG" | head -n1 || true)
    if [[ -n "$TUNNEL_URL" ]]; then
      echo "==> tunnel: $TUNNEL_URL"
      break
    fi
    sleep 0.5
  done
  if [[ -z "${TUNNEL_URL:-}" ]]; then
    echo "==> cloudflared didn't print a URL yet; tailing $TUNNEL_LOG…"
  fi
else
  echo "==> cloudflared not installed; skipping tunnel. Install: brew install cloudflared" >&2
fi

cleanup() {
  if [[ -n "$TUNNEL_PID" ]] && kill -0 "$TUNNEL_PID" 2>/dev/null; then
    echo "==> stopping cloudflared (pid $TUNNEL_PID)"
    kill "$TUNNEL_PID" 2>/dev/null || true
    wait "$TUNNEL_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

"$SCRIPT_DIR/run-docker.sh"
