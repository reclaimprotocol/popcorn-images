#!/usr/bin/env bash
# probe-fingerprint.sh — iterate random cloakbrowser seeds and print the
# WebGL renderer each one resolves to, so an integrated-GPU seed can be
# pinned via CLOAK_FINGERPRINT_SEED.
#
# Why this exists: cloakbrowser derives the spoofed `UNMASKED_RENDERER_WEBGL`
# string from `--fingerprint=<seed>` using an internal pool that includes
# flagship discrete GPUs ("NVIDIA GeForce RTX 4070 Ti" etc.). Under
# SwiftShader rasterization (this image's GPU stack), Akamai's WebGL pixel
# readback hashes to the SwiftShader signature, which can't have come from
# an NVIDIA driver — a deterministic bot tell that soft-fails Delta login.
# Pinning a seed that maps to an Intel integrated GPU collapses that gap:
# the spoofed renderer string is plausible for software output.
#
# Usage (run on the host that already has a chromium-headful container up):
#
#   ./scripts/probe-fingerprint.sh                       # 10 attempts, default container
#   COUNT=30 ./scripts/probe-fingerprint.sh              # 30 attempts
#   CONTAINER=chromium-headful-test ./scripts/probe-fingerprint.sh
#
# Output: one line per attempt with `seed=<n> gpu=<renderer> verdict=<ok|bad>`.
# At the end it prints any seeds whose verdict was `ok` — pin one via:
#
#   docker run -e CLOAK_FINGERPRINT_SEED=<seed> ...
#
# or set it in run-docker.sh / your deployment.

set -euo pipefail

CONTAINER="${CONTAINER:-chromium-headful-test}"
COUNT="${COUNT:-10}"
# What counts as a "safe" GPU profile. Integrated Intel + low-end older
# discrete cards have pixel output closest to SwiftShader. Update if you
# discover other classes that pass Delta in practice.
SAFE_REGEX="${SAFE_REGEX:-Intel.*(UHD|Iris|HD Graphics)|Mesa|llvmpipe|SwiftShader}"
# Hard-rejects: flagship discrete GPUs the SwiftShader-vs-claim mismatch
# bites worst on.
BAD_REGEX="${BAD_REGEX:-RTX [0-9]{4}|GTX 1[0-9]{3}|RX [567890][0-9]{3}|Radeon Pro|Quadro}"

if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  echo "error: container '$CONTAINER' is not running. Start it first or set CONTAINER=..." >&2
  exit 1
fi

# Inside the container, we already have a cloakbrowser at /usr/bin/chromium.
# For each candidate seed, launch it headless with that seed, point CDP at a
# data: URL that reports the unmasked WebGL renderer in document.title, scrape
# the title, kill the process. ~3s per attempt.
PROBE_SCRIPT='
set -e
SEED=$1
PORT=$((9300 + RANDOM % 100))
# Match the production GPU stack so the resolved renderer string is what the
# real container would expose. Without these flags chromium picks a different
# GL backend and the seed→renderer mapping shifts.
chromium \
  --headless=new \
  --no-sandbox \
  --disable-dev-shm-usage \
  --remote-debugging-port=$PORT \
  --disable-gpu \
  --use-gl=swiftshader \
  --enable-unsafe-swiftshader \
  --fingerprint=$SEED \
  --fingerprint-platform=windows \
  "data:text/html,<script>const c=document.createElement(%22canvas%22);const g=c.getContext(%22webgl%22);const e=g.getExtension(%22WEBGL_debug_renderer_info%22);document.title=g.getParameter(e.UNMASKED_RENDERER_WEBGL);</script>" \
  >/dev/null 2>&1 &
PID=$!
# Wait for CDP to come up, then read page title.
for i in 1 2 3 4 5 6 7 8 9 10; do
  TARGETS=$(curl -s --max-time 1 http://127.0.0.1:$PORT/json 2>/dev/null || true)
  if [[ -n "$TARGETS" ]]; then
    TITLE=$(echo "$TARGETS" | awk -F\" "/\"title\"/ {print \$4; exit}")
    if [[ -n "$TITLE" && "$TITLE" != "about:blank" ]]; then
      echo "$TITLE"
      kill $PID 2>/dev/null || true
      wait $PID 2>/dev/null || true
      exit 0
    fi
  fi
  sleep 0.3
done
kill $PID 2>/dev/null || true
wait $PID 2>/dev/null || true
echo "(probe timed out)"
'

OK_SEEDS=()
echo "Probing $COUNT random seeds via container '$CONTAINER'..."
for i in $(seq 1 "$COUNT"); do
  SEED=$(( $(od -An -N4 -tu4 < /dev/urandom | tr -d ' ') % 9000000 + 1000000 ))
  GPU=$(docker exec "$CONTAINER" bash -c "$PROBE_SCRIPT" -- "$SEED" 2>/dev/null || echo "(error)")
  VERDICT="?"
  if [[ "$GPU" =~ $BAD_REGEX ]]; then
    VERDICT="bad"
  elif [[ "$GPU" =~ $SAFE_REGEX ]]; then
    VERDICT="ok"
    OK_SEEDS+=("$SEED:$GPU")
  else
    VERDICT="unknown"
  fi
  printf 'seed=%-8s verdict=%-7s gpu=%s\n' "$SEED" "$VERDICT" "$GPU"
done

echo
if [[ ${#OK_SEEDS[@]} -eq 0 ]]; then
  echo "No safe seeds found in $COUNT attempts. Increase COUNT or widen SAFE_REGEX."
  exit 1
fi
echo "Safe seeds (pin one via CLOAK_FINGERPRINT_SEED=<seed>):"
printf '  %s\n' "${OK_SEEDS[@]}"
