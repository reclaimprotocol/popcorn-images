#!/bin/bash

set -o pipefail -o errexit -o nounset

# If the WITHDOCKER environment variable is not set, it means we are not running inside a Docker container.
# Docker manages /dev/shm itself, and attempting to mount or modify it can cause permission or device errors.
# However, in a unikernel container environment (non-Docker), we need to manually create and mount /dev/shm as a tmpfs
# to support shared memory operations.
if [ -z "${WITHDOCKER:-}" ]; then
  mkdir -p /dev/shm
  chmod 777 /dev/shm
  mount -t tmpfs tmpfs /dev/shm
fi

# We disable scale-to-zero for the lifetime of this script and restore
# the original setting on exit.
SCALE_TO_ZERO_FILE="/uk/libukp/scale_to_zero_disable"
scale_to_zero_write() {
  local char="$1"
  # Skip when not running inside Unikraft Cloud (control file absent)
  if [[ -e "$SCALE_TO_ZERO_FILE" ]]; then
    # Write the character, but do not fail the whole script if this errors out
    echo -n "$char" > "$SCALE_TO_ZERO_FILE" 2>/dev/null || \
      echo "[wrapper] Failed to write to scale-to-zero control file" >&2
  fi
}
disable_scale_to_zero() { scale_to_zero_write "+"; }
enable_scale_to_zero()  { scale_to_zero_write "-"; }

wait_for_tcp_port() {
  local host="$1"
  local port="$2"
  local name="$3"
  local attempts="${4:-0}"
  local sleep_secs="${5:-0.5}"
  local timeout_label="${6:-}"
  local attempt=0

  echo "[wrapper] Waiting for ${name} on ${host}:${port}..."
  while true; do
    if (echo >/dev/tcp/"${host}"/"${port}") >/dev/null 2>&1; then
      echo "[wrapper] ${name} is ready on ${host}:${port}"
      return 0
    fi

    if (( attempts > 0 )); then
      attempt=$((attempt + 1))
      if (( attempt >= attempts )); then
        if [[ -n "${timeout_label}" ]]; then
          echo "[wrapper] WARNING: ${name} not ready on ${host}:${port} after ${timeout_label}" >&2
        else
          echo "[wrapper] WARNING: ${name} not ready on ${host}:${port} after ${attempts} attempts" >&2
        fi
        return 1
      fi
    fi

    sleep "${sleep_secs}"
  done
}

# Disable scale-to-zero for the duration of the script when not running under Docker
if [[ -z "${WITHDOCKER:-}" ]]; then
  echo "[wrapper] Disabling scale-to-zero"
  disable_scale_to_zero
fi

# -----------------------------------------------------------------------------
# Ensure a sensible hostname ---------------------------------------------------
# -----------------------------------------------------------------------------
# Some environments boot with an empty or \"(none)\" hostname which shows up in
# prompts. Best-effort set a friendly hostname early so services inherit it.
if h=$(cat /proc/sys/kernel/hostname 2>/dev/null); then
  if [ -z "$h" ] || [ "$h" = "(none)" ]; then
    if command -v hostname >/dev/null 2>&1; then
      hostname kernel-vm 2>/dev/null || true
    fi
    echo -n "kernel-vm" > /proc/sys/kernel/hostname 2>/dev/null || true
  fi
fi
# Also export HOSTNAME so shells pick it up immediately.
export HOSTNAME="${HOSTNAME:-kernel-vm}"

# -----------------------------------------------------------------------------
# House-keeping for the unprivileged "kernel" user --------------------------------
# Some Chromium subsystems want to create files under $HOME (NSS cert DB, dconf
# cache).  If those directories are missing or owned by root Chromium emits
# noisy error messages such as:
#   [ERROR:crypto/nss_util.cc:48] Failed to create /home/kernel/.pki/nssdb ...
#   dconf-CRITICAL **: unable to create directory '/home/kernel/.cache/dconf'
# Pre-create them and hand ownership to the user so the messages disappear.
# When RUN_AS_ROOT is true, we skip ownership changes since we're running as root.

if [[ "${RUN_AS_ROOT:-}" != "true" ]]; then
  dirs=(
    /home/kernel/user-data
    /home/kernel/.config/chromium
    /home/kernel/.pki/nssdb
    /home/kernel/.cache/dconf
    /tmp
    /var/log
    /var/log/supervisord
  )

  for dir in "${dirs[@]}"; do
    if [ ! -d "$dir" ]; then
      mkdir -p "$dir"
    fi
  done

  # Ensure correct ownership (ignore errors if already correct)
  chown -R kernel:kernel /home/kernel /home/kernel/user-data /home/kernel/.config /home/kernel/.pki /home/kernel/.cache 2>/dev/null || true
  # Make policy directory writable for runtime updates
  chown -R kernel:kernel /etc/chromium/policies 2>/dev/null || true
else
  # When running as root, just create the necessary directories without ownership changes
  dirs=(
    /tmp
    /var/log
    /var/log/supervisord
    /home/kernel
    /home/kernel/user-data
  )

  for dir in "${dirs[@]}"; do
    if [ ! -d "$dir" ]; then
      mkdir -p "$dir"
    fi
  done
fi

# -----------------------------------------------------------------------------
# Dynamic log aggregation for /var/log/supervisord -----------------------------
# -----------------------------------------------------------------------------
# Tails any existing and future files under /var/log/supervisord,
# prefixing each line with the relative filepath, e.g. [chromium] ...
start_dynamic_log_aggregator() {
  echo "[wrapper] Starting dynamic log aggregator for /var/log/supervisord"
  (
    declare -A tailed_files=()
    start_tail() {
      local f="$1"
      [[ -f "$f" ]] || return 0
      [[ -n "${tailed_files[$f]:-}" ]] && return 0
      local label="${f#/var/log/supervisord/}"
      # Tie tails to this subshell lifetime so they exit when we stop it
      tail --pid="$$" -n +1 -F "$f" 2>/dev/null | sed -u "s/^/[${label}] /" &
      tailed_files[$f]=1
    }
    # Periodically scan for new *.log files without extra dependencies
    while true; do
      while IFS= read -r -d '' f; do
        start_tail "$f"
      done < <(find /var/log/supervisord -type f -print0 2>/dev/null || true)
      sleep 1
    done
  ) &
  tail_pids+=("$!")
}

# Start log aggregator early so we see supervisor and service logs as they appear
start_dynamic_log_aggregator

export DISPLAY=:1

# CloakBrowser stealth defaults. The javascript `launch()` wrapper normally
# injects these; we invoke the chromium binary directly via chromium-launcher,
# so we add them here. Without all four, Akamai/Cloudflare correlate the
# mismatch: --fingerprint-platform=windows spoofs navigator.platform and
# Sec-CH-UA-Platform to Windows, but Intl.DateTimeFormat().resolvedOptions()
# .timeZone, navigator.language, and Date arithmetic still report whatever
# the underlying Linux container says. A "Windows" UA reporting Etc/UTC and
# lang=C is the tell that closes the trap.
#
# Seed, timezone, and locale are all persisted in user-data so the spoofed
# identity stays stable across restarts. Flipping any of these against the
# same profile is itself a bot signal — bot detectors track per-cookie-jar
# fingerprint stability.
CLOAK_PROFILE_DIR="/home/kernel/user-data"
mkdir -p "$CLOAK_PROFILE_DIR"

# Profile seed bundle. If $CLOAK_PROFILE_SEED points at a tarball and the
# user-data dir looks empty, seed it. This is how cluster instances start
# pre-logged-in: bundle a curated profile-state.tar.gz on a successful
# manual login, ship that bundle alongside the image, and every fresh
# instance untars it as its starting state. Keeps cookies, localStorage,
# IndexedDB, and the persisted fingerprint seed intact.
#
# Skipped on subsequent boots (presence of .cloak-fingerprint-seed
# indicates the profile is already initialized), so a successful 2FA
# session inside the cluster instance accumulates further state without
# being clobbered by the original seed bundle.
#
# The bundle is a *credential* — it carries auth cookies for whatever
# sites you logged into. Treat it like a password: never commit, never
# distribute over plain HTTP, rotate when it expires.
if [[ -n "${CLOAK_PROFILE_SEED:-}" ]] \
   && [[ -f "$CLOAK_PROFILE_SEED" ]] \
   && [[ ! -e "$CLOAK_PROFILE_DIR/.cloak-fingerprint-seed" ]] \
   && [[ ! -e "$CLOAK_PROFILE_DIR/Default" ]]; then
  echo "[wrapper] seeding profile from $CLOAK_PROFILE_SEED"
  tar -xzf "$CLOAK_PROFILE_SEED" -C "$CLOAK_PROFILE_DIR" \
    && echo "[wrapper] profile seed restored" \
    || echo "[wrapper] profile seed extraction failed (continuing with empty profile)" >&2
fi

CLOAK_SEED_FILE="$CLOAK_PROFILE_DIR/.cloak-fingerprint-seed"
if [[ -s "$CLOAK_SEED_FILE" ]]; then
  CLOAK_SEED=$(cat "$CLOAK_SEED_FILE")
else
  # CSPRNG seed (was bash $RANDOM, which is 15-bit and seeds collide across
  # fleet workers within hours — Akamai correlates _abck cookies on identical
  # fingerprint seeds. /dev/urandom gives 32-bit entropy with negligible
  # collision probability across thousands of pods.
  CLOAK_SEED=$(( $(od -An -N4 -tu4 < /dev/urandom | tr -d ' ') % 9000000 + 1000000 ))
  echo "$CLOAK_SEED" > "$CLOAK_SEED_FILE"
fi

# geoip=True equivalent. CloakBrowser's launch() wrapper has a `geoip=True`
# option that resolves the proxy exit IP to a timezone + locale at startup
# and pins the fingerprint to those values. We're not using launch(), so we
# replicate it inline: query ipapi.co once on first boot, derive timezone
# from the response, derive a primary locale from `languages`, and persist.
# Akamai/Cloudflare grade higher when the spoofed timezone+locale match the
# IP's geographical region — running a residential proxy in Frankfurt while
# reporting `Etc/UTC` + `en-US` is the tell. Default on; flip off with
# CLOAK_GEOIP=false if you want to pin manually via CLOAK_TIMEZONE / CLOAK_LOCALE.
CLOAK_GEOIP="${CLOAK_GEOIP:-true}"
CLOAK_TZ_FILE="$CLOAK_PROFILE_DIR/.cloak-fingerprint-timezone"
CLOAK_LOCALE_FILE="$CLOAK_PROFILE_DIR/.cloak-fingerprint-locale"

if [[ ! -s "$CLOAK_TZ_FILE" || ! -s "$CLOAK_LOCALE_FILE" ]] \
   && [[ "$CLOAK_GEOIP" == "true" ]]; then
  echo "[wrapper] geoip: resolving timezone/locale from current exit IP…"
  # 5s timeout; fail open. ipapi.co/json returns flat JSON with `timezone`
  # and CSV `languages` ("en-US,es-US,..."). Parse with awk to avoid a jq
  # dependency. The 1000-req/day rate limit is plenty for first-boot use.
  GEOIP_JSON=$(curl -s --max-time 5 https://ipapi.co/json/ 2>/dev/null || true)
  if [[ -n "$GEOIP_JSON" ]]; then
    GEOIP_TZ=$(echo "$GEOIP_JSON" \
      | awk -F'"' '/"timezone"[[:space:]]*:/ {print $4; exit}')
    GEOIP_LANGS=$(echo "$GEOIP_JSON" \
      | awk -F'"' '/"languages"[[:space:]]*:/ {print $4; exit}')
    GEOIP_LOCALE=$(echo "$GEOIP_LANGS" | awk -F',' '{print $1}')
    [[ -n "$GEOIP_TZ" ]]     && CLOAK_TIMEZONE="${CLOAK_TIMEZONE:-$GEOIP_TZ}"
    [[ -n "$GEOIP_LOCALE" ]] && CLOAK_LOCALE="${CLOAK_LOCALE:-$GEOIP_LOCALE}"
    echo "[wrapper] geoip: timezone=${CLOAK_TIMEZONE:-?} locale=${CLOAK_LOCALE:-?}"
  else
    echo "[wrapper] geoip: lookup failed (offline or rate-limited); falling back to env/defaults"
  fi
fi

# Persisted-file priority: file → env (or geoip-resolved env) → fallback.
# Once persisted, the value sticks across restarts regardless of env or
# geoip changes — the spoofed identity must remain stable for the same
# cookie jar.
if [[ -s "$CLOAK_TZ_FILE" ]]; then
  CLOAK_TIMEZONE=$(cat "$CLOAK_TZ_FILE")
else
  CLOAK_TIMEZONE="${CLOAK_TIMEZONE:-${TZ:-America/Los_Angeles}}"
  echo "$CLOAK_TIMEZONE" > "$CLOAK_TZ_FILE"
fi

if [[ -s "$CLOAK_LOCALE_FILE" ]]; then
  CLOAK_LOCALE=$(cat "$CLOAK_LOCALE_FILE")
else
  CLOAK_LOCALE="${CLOAK_LOCALE:-en-US}"
  echo "$CLOAK_LOCALE" > "$CLOAK_LOCALE_FILE"
fi

# Align the OS-level TZ and zoneinfo so getTimezoneOffset() / Date arithmetic
# match the spoofed Intl timezone. Without this, JS computing offset from
# `new Date()` reads the container TZ while Intl reports the spoofed value —
# a one-line correlation Akamai catches.
export TZ="$CLOAK_TIMEZONE"
if [[ -r "/usr/share/zoneinfo/$CLOAK_TIMEZONE" ]]; then
  ln -sf "/usr/share/zoneinfo/$CLOAK_TIMEZONE" /etc/localtime 2>/dev/null || true
  echo "$CLOAK_TIMEZONE" > /etc/timezone 2>/dev/null || true
fi

export CHROMIUM_FLAGS="${CHROMIUM_FLAGS:-} \
  --fingerprint=${CLOAK_SEED} \
  --fingerprint-platform=windows \
  --fingerprint-timezone=${CLOAK_TIMEZONE} \
  --fingerprint-locale=${CLOAK_LOCALE} \
  --lang=${CLOAK_LOCALE} \
  --ignore-gpu-blocklist"

# WebRTC public-IP alignment. CloakBrowser v0.3.26+ supports
# `--fingerprint-webrtc-ip=auto` which resolves the proxy exit IP at runtime
# (via STUN through the configured proxy). With geoip on, this is the right
# default — STUN-leaked srflx candidates would otherwise betray the container's
# real IP. Set CLOAK_WEBRTC_IP to a literal IP to override the auto resolution.
if [[ -n "${CLOAK_WEBRTC_IP:-}" ]]; then
  export CHROMIUM_FLAGS="${CHROMIUM_FLAGS} --fingerprint-webrtc-ip=${CLOAK_WEBRTC_IP}"
elif [[ "$CLOAK_GEOIP" == "true" ]]; then
  export CHROMIUM_FLAGS="${CHROMIUM_FLAGS} --fingerprint-webrtc-ip=auto"
fi

# Stealth status banner — surfaces what's active for ops debugging. If you ever
# get caught, the first thing to verify is whether all four of these are on.
# Anything missing is a deterministic Akamai BMP detection vector.
echo "[stealth] CloakBrowser fingerprint: seed=${CLOAK_SEED} platform=windows tz=${CLOAK_TIMEZONE} locale=${CLOAK_LOCALE}"
echo "[stealth] geoip resolution: ${CLOAK_GEOIP}"
echo "[stealth] WebRTC IP spoof: ${CLOAK_WEBRTC_IP:-${CLOAK_GEOIP:+auto}${CLOAK_GEOIP:-disabled}}"
echo "[stealth] page-world overrides: outerHeight/Width clamped (kiosk geometry leak fix)"
echo "[stealth] consumer must apply: patchBrowser(browser, resolveConfig('default')) for humanize"

# Set default extension flags for bundled extensions
export CHROMIUM_FLAGS="${CHROMIUM_FLAGS:-} --disable-extensions-except=/home/kernel/extensions/proxy --load-extension=/home/kernel/extensions/proxy"

# Resource-optimization flags — apply to *every* environment (local docker,
# K8s/Agones fleet, unikernel) because they live in wrapper.sh rather than the
# host-side run-docker.sh. Tuned for the 2 vCPU / 3 GiB Agones pod budget.
#
# CRITICAL CONSTRAINT: every flag here has been audited against Akamai BMP's
# JS-side detection surface. Anything that mutates an API a real Chrome page
# can observe is excluded. The kept set produces no JS-visible side effect:
# they trim background services, telemetry, internal feature flags, and
# the GPU-process subsystem. Real Chrome's navigator.plugins, chrome.cast,
# popup-blocker behavior, and site-isolation boundary all stay intact, which
# is what BMP fingerprints against.
#
# Specifically NOT included even though they'd save memory/CPU:
#   --disable-features=MediaRouter         → would break chrome.cast.isAvailable()
#   --disable-component-extensions-with-background-pages
#                                          → would empty navigator.plugins of PDF viewer
#   --disable-site-isolation-trials        → core site-per-process stays on, but BMP
#                                            could probe trial-only behaviors
#   --disable-popup-blocking               → real Chrome has blocking on by default;
#                                            window.open behavior diverges if disabled
#
# GPU stack: --disable-gpu + --use-gl=swiftshader pairs SwiftShader-software
# WebGL with a CPU compositor. Crucial: SwiftShader keeps WebGL functional
# (CloakBrowser's renderer-string spoof needs a real context to overlay), while
# the GPU process is skipped so the encoder's CPU isn't fighting GL emulation.
# --enable-unsafe-swiftshader is required on chromium 117+ for SwiftShader to
# bind without --enable-gpu.
#
# Memory: --renderer-process-limit caps process count so a misbehaving site
# can't fork its way past the 3 GiB pod limit. Site isolation stays on by
# default — we don't override it.
export CHROMIUM_FLAGS="${CHROMIUM_FLAGS:-} \
  --disable-gpu \
  --disable-gpu-compositing \
  --enable-unsafe-swiftshader \
  --use-gl=swiftshader \
  --enable-zero-copy \
  --in-process-gpu \
  --renderer-process-limit=4 \
  --mute-audio \
  --disable-background-networking \
  --disable-breakpad \
  --disable-client-side-phishing-detection \
  --disable-component-update \
  --disable-default-apps \
  --disable-domain-reliability \
  --disable-features=Translate,OptimizationGuideModelDownloading,InterestFeedContentSuggestions,SidePanelPinning,DownloadBubble,DialMediaRouteProvider,CalculateNativeWinOcclusion \
  --disable-hang-monitor \
  --disable-prompt-on-repost \
  --disable-sync \
  --metrics-recording-only \
  --no-default-browser-check \
  --noerrdialogs \
  --use-mock-keychain"

# Predefine ports and export for services
export INTERNAL_PORT="${INTERNAL_PORT:-9223}"
export CHROME_PORT="${CHROME_PORT:-9222}"

# Track background tailing processes for cleanup
tail_pids=()

# Cleanup handler (set early so we catch early failures)
cleanup () {
  echo "[wrapper] Cleaning up..."
  # Re-enable scale-to-zero if the script terminates early
  enable_scale_to_zero
  supervisorctl -c /etc/supervisor/supervisord.conf stop chromedriver || true
  supervisorctl -c /etc/supervisor/supervisord.conf stop chromium || true
  supervisorctl -c /etc/supervisor/supervisord.conf stop kernel-images-api || true
  supervisorctl -c /etc/supervisor/supervisord.conf stop dbus || true
  # Stop log tailers
  if [[ -n "${tail_pids[*]:-}" ]]; then
    for tp in "${tail_pids[@]}"; do
      kill -TERM "$tp" 2>/dev/null || true
    done
  fi
}
trap cleanup TERM INT

# Start supervisord early so it can manage Xorg and Mutter
echo "[wrapper] Starting supervisord"
supervisord -c /etc/supervisor/supervisord.conf
echo "[wrapper] Waiting for supervisord socket..."
for i in {1..30}; do
if [ -S /var/run/supervisor.sock ]; then
    break
fi
sleep 0.2
done

echo "[wrapper] Starting Xorg via supervisord"
supervisorctl -c /etc/supervisor/supervisord.conf start xorg
echo "[wrapper] Waiting for Xorg to open display $DISPLAY..."
for i in {1..50}; do
  if xdpyinfo -display "$DISPLAY" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

echo "[wrapper] Starting Mutter via supervisord"
supervisorctl -c /etc/supervisor/supervisord.conf start mutter
echo "[wrapper] Waiting for Mutter to be ready..."
timeout=30
while [ $timeout -gt 0 ]; do
  if xdotool search --class "mutter" >/dev/null 2>&1; then
    break
  fi
  sleep 1
  ((timeout--))
done

# -----------------------------------------------------------------------------
# System-bus setup via supervisord --------------------------------------------
# -----------------------------------------------------------------------------
echo "[wrapper] Starting system D-Bus daemon via supervisord"
supervisorctl -c /etc/supervisor/supervisord.conf start dbus
echo "[wrapper] Waiting for D-Bus system bus socket..."
for i in {1..50}; do
  if [ -S /run/dbus/system_bus_socket ]; then
    break
  fi
  sleep 0.2
done

# We will point DBUS_SESSION_BUS_ADDRESS at the system bus socket to suppress
# autolaunch attempts that failed and spammed logs.
export DBUS_SESSION_BUS_ADDRESS="unix:path=/run/dbus/system_bus_socket"

# Start Chromium with display :1 and remote debugging, loading our recorder extension.
echo "[wrapper] Starting Chromium via supervisord on internal port $INTERNAL_PORT"
supervisorctl -c /etc/supervisor/supervisord.conf start chromium
wait_for_tcp_port 127.0.0.1 "$INTERNAL_PORT" "Chromium remote debugging" 100 0.2 "20s" || true

if [[ "${ENABLE_WEBRTC:-}" == "true" ]]; then
  # use webrtc
  echo "[wrapper] ✨ Starting neko (webrtc server) via supervisord."
  supervisorctl -c /etc/supervisor/supervisord.conf start neko

  # Wait for neko to be ready.
  wait_for_tcp_port 127.0.0.1 8080 "neko"
fi

echo "[wrapper] ✨ Starting kernel-images API."

API_PORT="${KERNEL_IMAGES_API_PORT:-10001}"
API_FRAME_RATE="${KERNEL_IMAGES_API_FRAME_RATE:-10}"
API_DISPLAY_NUM="${KERNEL_IMAGES_API_DISPLAY_NUM:-${DISPLAY_NUM:-1}}"
API_MAX_SIZE_MB="${KERNEL_IMAGES_API_MAX_SIZE_MB:-500}"
API_OUTPUT_DIR="${KERNEL_IMAGES_API_OUTPUT_DIR:-/recordings}"

# Start via supervisord (env overrides are read by the service's command)
supervisorctl -c /etc/supervisor/supervisord.conf start kernel-images-api
wait_for_tcp_port 127.0.0.1 "${API_PORT}" "kernel-images API"

# ChromeDriver is only useful for WebDriver-protocol clients (Selenium etc.).
# Production consumers connect via direct CDP, so chromedriver is dead weight
# in the cluster — ~50MB RAM and a JVM-style process group. Default off; set
# ENABLE_CHROMEDRIVER=true for local dev or selenium-style use.
if [[ "${ENABLE_CHROMEDRIVER:-false}" == "true" ]]; then
  echo "[wrapper] Starting ChromeDriver via supervisord"
  supervisorctl -c /etc/supervisor/supervisord.conf start chromedriver
  wait_for_tcp_port 127.0.0.1 9225 "ChromeDriver" 50 0.2 "10s" || true
else
  echo "[wrapper] ChromeDriver disabled (ENABLE_CHROMEDRIVER!=true)"
fi

# PulseAudio: chromium runs with --mute-audio so there's no audio output to
# route anywhere. PulseAudio is purely overhead in that case (~30MB and a
# busy-loop service). Default off; set ENABLE_PULSEAUDIO=true if you actually
# need audio capture.
if [[ "${ENABLE_PULSEAUDIO:-false}" == "true" ]]; then
  echo "[wrapper] Starting PulseAudio daemon via supervisord"
  supervisorctl -c /etc/supervisor/supervisord.conf start pulseaudio
else
  echo "[wrapper] PulseAudio disabled (ENABLE_PULSEAUDIO!=true)"
fi

# close the "--no-sandbox unsupported flag" warning when running as root
# in the unikernel runtime we haven't been able to get chromium to launch as non-root without cryptic crashpad errors
# and when running as root you must use the --no-sandbox flag, which generates a warning
if [[ "${RUN_AS_ROOT:-}" == "true" ]]; then
  echo "[wrapper] Running as root, attempting to dismiss the --no-sandbox unsupported flag warning"
  if read -r WIDTH HEIGHT <<< "$(xdotool getdisplaygeometry 2>/dev/null)"; then
    # Work out an x-coordinate slightly inside the right-hand edge of the
    OFFSET_X=$(( WIDTH - 30 ))
    if (( OFFSET_X < 0 )); then
      OFFSET_X=0
    fi

    # Wait for a Chromium window to open before dismissing the --no-sandbox warning.
    # Match is loose because stealth builds (e.g. CloakBrowser) may rebrand the title.
    target='New Tab|Chromium|Chrome'
    echo "[wrapper] Waiting for Chromium window matching /${target}/ to appear and become active..."
    window_found=false
    for _ in $(seq 1 60); do
      # `|| true` so a failing xwininfo/awk (under set -e + pipefail) doesn't kill the wrapper
      win_id=$( { xwininfo -root -tree 2>/dev/null | awk -v t="$target" '$0 ~ t {print $1; exit}'; } || true )
      if [[ -n $win_id ]]; then
        win_id=${win_id%:}
        if xdotool windowactivate --sync "$win_id" 2>/dev/null; then
          echo "[wrapper] Focused window $win_id on $DISPLAY"
          window_found=true
          break
        fi
      fi
      sleep 0.5
    done
    if [[ "$window_found" != "true" ]]; then
      echo "[wrapper] No Chromium window matched within 30s; skipping sandbox warning dismissal." >&2
    fi

    if [[ "$window_found" == "true" ]]; then
      # wait... not sure but this just increases the likelihood of success
      # without the sleep you often open the live view and see the mouse hovering over the "X" to dismiss the warning, suggesting that it clicked before the warning or chromium appeared
      sleep 5

      # Attempt to click the warning's close button
      echo "[wrapper] Clicking the warning's close button at x=$OFFSET_X y=115"
      if curl -s -o /dev/null -X POST \
        http://localhost:${API_PORT}/computer/click_mouse \
        -H "Content-Type: application/json" \
        -d "{\"x\":${OFFSET_X},\"y\":115}"; then
          echo "[wrapper] Successfully clicked the warning's close button"
      else
        echo "[wrapper] Failed to click the warning's close button" >&2
      fi
    fi
  else
    echo "[wrapper] xdotool failed to obtain display geometry; skipping sandbox warning dismissal." >&2
  fi
fi

if [[ -z "${WITHDOCKER:-}" ]]; then
  enable_scale_to_zero
fi

# Keep the container running while streaming logs
wait
