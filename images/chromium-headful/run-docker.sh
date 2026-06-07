#!/usr/bin/env bash
set -ex -o pipefail

# Move to the script's directory so relative paths work regardless of the caller CWD
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
cd "$SCRIPT_DIR"
source ../../shared/ensure-common-build-run-vars.sh chromium-headful

# Directory on host where recordings will be saved
HOST_RECORDINGS_DIR="$SCRIPT_DIR/recordings"
mkdir -p "$HOST_RECORDINGS_DIR"

# Directory on host where the chromium profile (cookies, localStorage,
# IndexedDB, password store, persisted CloakBrowser fingerprint seed) is
# stored. Bind-mounting this is what makes "still logged in after restart"
# work — without it, every `docker rm -f` at the end of this script wipes
# the profile. Override with PERSIST_PROFILE=false to start clean each run.
HOST_USER_DATA_DIR="${HOST_USER_DATA_DIR:-$SCRIPT_DIR/.user-data}"
if [[ "${PERSIST_PROFILE:-true}" == "true" ]]; then
  mkdir -p "$HOST_USER_DATA_DIR"
fi

# RUN_AS_ROOT defaults to false in docker
RUN_AS_ROOT="${RUN_AS_ROOT:-false}"

# Build Chromium flags file and mount.
#
# Geometry: --kiosk under Xdummy produces window.outerHeight < innerHeight,
# which is physically impossible on real Chrome and a deterministic Akamai BMP
# bot signal. The kiosk flag itself comes from chromium-launcher; we counteract
# the geometry leak in extensions/proxy/page.js by overriding the outerHeight
# getter at document_start (clamps to inner whenever chromium reports a
# negative delta). --start-maximized + --window-size are harmless under kiosk
# but kept as defense-in-depth in case kiosk is disabled.
#
# GPU/rendering tradeoff: --disable-gpu alone (without --disable-software-
# rasterizer) lets chromium fall back to SwiftShader for WebGL while skipping
# the heavy GPU-compositor emulation path. CloakBrowser's seed-based renderer-
# string spoof activates on any WebGL context, including SwiftShader, so the
# UNMASKED_RENDERER_WEBGL fingerprint remains a realistic Windows NVIDIA
# string. Net effect: CPU usage drops materially (Neko's WebRTC video encoder
# stops fighting the chromium compositor) and the cursor becomes responsive,
# without losing the fingerprint signal Akamai checks.
#
# Setting --disable-gpu-compositing on top of --disable-gpu pushes the
# compositor entirely to the CPU's raster path (Skia software), which is
# lighter than the GPU-process emulation chromium otherwise spins up.
# Local-dev-only flags. Resource-optimization and stealth flags now live in
# wrapper.sh's CHROMIUM_FLAGS, which applies in *every* deployment (local docker,
# K8s/Agones, unikernel) instead of just here. Keep this list minimal — only
# flags that genuinely vary by docker-vs-cluster: viewport sizing, dev shm,
# the no-sandbox pair we add when running as root.
CHROMIUM_FLAGS_DEFAULT="\
--user-data-dir=/home/kernel/user-data \
--disable-dev-shm-usage \
--start-maximized \
--window-size=1920,1040 \
--remote-allow-origins=*"
if [[ "$RUN_AS_ROOT" == "true" ]]; then
  CHROMIUM_FLAGS_DEFAULT="$CHROMIUM_FLAGS_DEFAULT --no-sandbox --no-zygote"
fi
CHROMIUM_FLAGS="${CHROMIUM_FLAGS:-$CHROMIUM_FLAGS_DEFAULT}"
rm -rf .tmp/chromium
mkdir -p .tmp/chromium
FLAGS_FILE="$(pwd)/.tmp/chromium/flags"

# Convert space-separated flags to JSON array format, handling quoted strings
# Use eval to properly parse quoted strings (respects shell quoting)
if [ -n "$CHROMIUM_FLAGS" ]; then
  eval "FLAGS_ARRAY=($CHROMIUM_FLAGS)"
else
  FLAGS_ARRAY=()
fi

FLAGS_JSON='{"flags":['
FIRST=true
for flag in "${FLAGS_ARRAY[@]}"; do
  if [ -n "$flag" ]; then
    if [ "$FIRST" = true ]; then
      FLAGS_JSON+="\"$flag\""
      FIRST=false
    else
      FLAGS_JSON+=",\"$flag\""
    fi
  fi
done
FLAGS_JSON+=']}'
echo "$FLAGS_JSON" > "$FLAGS_FILE"

echo "flags file: $FLAGS_FILE"
cat "$FLAGS_FILE"

# Build docker run argument list
RUN_ARGS=(
  --name "$NAME"
  --privileged
  --tmpfs /dev/shm:size=2g
  -v "$HOST_RECORDINGS_DIR:/recordings"
  --memory 3072m
  -p 9222:9222
  -p 9224:9224
  -p 9226:9226
  -p 444:10001
  -e DISPLAY_NUM=1
  -e HEIGHT=1080
  -e WIDTH=1920
  -e TZ=${TZ:-'America/Los_Angeles'}
  -e RUN_AS_ROOT="$RUN_AS_ROOT"
  --mount type=bind,src="$FLAGS_FILE",dst=/chromium/flags
)

if [[ "${PERSIST_PROFILE:-true}" == "true" ]]; then
  RUN_ARGS+=( -v "$HOST_USER_DATA_DIR:/home/kernel/user-data" )
fi

# Seed the profile from a bundled tarball produced by:
#
#   docker exec chromium-headful-test bash -c '
#     cd /home/kernel/user-data && tar \
#       --exclude="Default/Cache" --exclude="Default/Code Cache" \
#       --exclude="Default/GPUCache" --exclude="Default/Service Worker/CacheStorage" \
#       --exclude="Default/Application Cache" --exclude="Default/Media Cache" \
#       --exclude="Default/Crash*" --exclude="ShaderCache" --exclude="GraphiteDawnCache" \
#       -czf /tmp/profile-state.tar.gz Default .cloak-fingerprint-* 2>/dev/null
#   ' && docker cp chromium-headful-test:/tmp/profile-state.tar.gz ./profile-state.tar.gz
#
# Then on a fresh launch:
#
#   PROFILE_SEED=./profile-state.tar.gz ./run-docker.sh
#
# wrapper.sh untars on first boot only — if the profile dir already has
# state, the bundle is ignored so re-launches don't clobber accumulated
# session updates (Akamai's _abck rotates per request, you want the latest).
if [[ -n "${PROFILE_SEED:-}" && -f "${PROFILE_SEED}" ]]; then
  RUN_ARGS+=(
    -v "$(cd "$(dirname "$PROFILE_SEED")" && pwd)/$(basename "$PROFILE_SEED"):/seed/profile-state.tar.gz:ro"
    -e "CLOAK_PROFILE_SEED=/seed/profile-state.tar.gz"
  )
fi

if [[ -n "${PLAYWRIGHT_ENGINE:-}" ]]; then
  RUN_ARGS+=( -e PLAYWRIGHT_ENGINE="$PLAYWRIGHT_ENGINE" )
fi


# TURN is only needed when the WebRTC client and the Neko server can't reach
# each other directly (they're behind separate NATs, on different networks).
# For local docker, both ends are on the host so the NAT1TO1=127.0.0.1
# fallback below works without any external TURN allocation.
#
# When TURN_KEY_ID is set, the script fetches Cloudflare TURN credentials
# and forces ICE to use them — if the container's network can't reach
# turn.cloudflare.com (firewall, ISP routing quirks, rate-limit), every
# allocation times out and the WebRTC peer connection drops, looking like
# a "server crash" in the Neko logs (read udp4 i/o timeout, "all
# retransmissions failed for ..."). Default to off; set TURN_KEY_ID +
# TURN_API_TOKEN explicitly only when deploying to a routed setup that
# genuinely needs it.
#
# These were previously hardcoded credentials committed to the repo. If you
# need them for cluster deployments, source them from your secrets manager
# rather than defaulting them in this script.
TURN_KEY_ID="${TURN_KEY_ID:-}"
TURN_API_TOKEN="${TURN_API_TOKEN:-}"

if [ ! -z "$TURN_KEY_ID" ] && [ ! -z "$TURN_API_TOKEN" ]; then
    echo "🔄 Fetching TURN credentials from Cloudflare..."
    RESPONSE=$(curl -s -X POST \
        -H "Authorization: Bearer $TURN_API_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"ttl": 86400}' \
        "https://rtc.live.cloudflare.com/v1/turn/keys/$TURN_KEY_ID/credentials/generate-ice-servers")

    # Extract iceServers array from response
    GENERATED_ICE_SERVERS=$(echo "$RESPONSE" | jq -c '.iceServers')

    if [ "$GENERATED_ICE_SERVERS" != "null" ] && [ ! -z "$GENERATED_ICE_SERVERS" ]; then
        export NEKO_ICESERVERS="$GENERATED_ICE_SERVERS"
        echo "✅ NEKO_ICESERVERS configured dynamically from Cloudflare."
    else
        echo "❌ Failed to fetch TURN credentials. Response: $RESPONSE"
    fi
fi

# WebRTC port mapping
if [[ "${ENABLE_WEBRTC:-}" == "true" ]]; then
  echo "Running container with WebRTC"
  RUN_ARGS+=( -p 8080:8080 )
  RUN_ARGS+=( -e ENABLE_WEBRTC=true )
  if [[ -n "${NEKO_ICESERVERS:-}" ]]; then
    RUN_ARGS+=( -e NEKO_ICESERVERS="$NEKO_ICESERVERS" )
  else
    RUN_ARGS+=( -e NEKO_WEBRTC_EPR=56000-56100 )
    RUN_ARGS+=( -e NEKO_WEBRTC_NAT1TO1=127.0.0.1 )
    RUN_ARGS+=( -p 56000-56100:56000-56100/udp )
  fi
fi

docker rm -f "$NAME" 2>/dev/null || true
docker run -it "${RUN_ARGS[@]}" "$IMAGE"
