#!/usr/bin/env bash
#
# Capture a warm chromium profile for use as a CLOAK_PROFILE_SEED bundle.
#
# Why this exists: Akamai BMP at airline tier (Delta, Finnair, ANA, BA)
# applies deceptive credential-stuffing protection on auth POSTs from
# fingerprints with no prior successful-action history. Cold-start
# sessions get rejected with "wrong credentials" even when credentials
# are correct. This is by design — Akamai prevents attackers from
# distinguishing "wrong password" from "bot detected."
#
# The fix is to never let a worker face the cold-start gate. A seeder
# pod logs in successfully ONCE per account (carefully, with humanize +
# residential proxy + behavioral preamble), captures the resulting
# profile state, and distributes it as a bundle. Worker pods boot with
# CLOAK_PROFILE_SEED=<bundle> and inherit the warm session — Akamai
# sees a returning visitor on every worker, deceptive rejection never
# fires.
#
# Usage from host (after a successful seeder login completed in the
# running container, ideally supervised manually via the Neko viewer):
#
#   docker exec popcorn-seeder /scripts/seed-profile.sh /tmp/profile.tar.gz
#   docker cp popcorn-seeder:/tmp/profile.tar.gz ./profile-state.tar.gz
#   age -e -r <recipient> -o profile-state.tar.gz.age profile-state.tar.gz
#   aws s3 cp profile-state.tar.gz.age s3://bot-fleet/profiles/${ACCT}.tar.gz.age
#
# The bundle is a CREDENTIAL — it carries live auth + Akamai cookies for
# whichever account was logged in. Treat like a password. Never commit,
# never distribute over plain HTTP, encrypt at rest.

set -euo pipefail

OUTPUT="${1:-/tmp/profile-state.tar.gz}"
PROFILE_DIR="/home/kernel/user-data"

if [[ ! -d "$PROFILE_DIR" ]]; then
    echo "ERROR: profile dir $PROFILE_DIR not found" >&2
    exit 1
fi

# Verify there's actually session state to capture. A profile with only a
# fingerprint seed and no Default/Cookies SQLite is empty — bundling that
# defeats the purpose. Check both that Default/Cookies exists and that it
# has size > 4KB (an empty Cookies DB is ~4KB; populated is much larger).
if [[ ! -f "$PROFILE_DIR/Default/Cookies" ]]; then
    echo "ERROR: $PROFILE_DIR/Default/Cookies not found — no session to capture." >&2
    echo "Run a successful login in this container first (Neko viewer or programmatic)" >&2
    echo "before invoking seed-profile.sh." >&2
    exit 1
fi

cookies_size=$(stat -c%s "$PROFILE_DIR/Default/Cookies" 2>/dev/null || stat -f%z "$PROFILE_DIR/Default/Cookies")
if [[ $cookies_size -lt 8192 ]]; then
    echo "WARNING: Cookies DB is small (${cookies_size} bytes) — session may be empty." >&2
    echo "Verify a successful login before continuing. Pass --force to bundle anyway." >&2
    if [[ "${2:-}" != "--force" ]]; then
        exit 1
    fi
fi

# Capture cookies, localStorage, IndexedDB, AND the HTTP cache (Default/Cache).
# Why include the cache: Akamai's edge logs see "this client requested every
# CSS/JS/font/image fresh" as a fresh-browser signal even when cookies show
# returning visitor. Including the cache makes the bundle's network behavior
# match a real return-visit (304 If-None-Match revalidations instead of full
# fetches). Bundle size grows ~50-150 MB but the auth-POST trust signal
# improves materially.
#
# Still excluded: GPU/shader caches (regenerable, hardware-specific so they'd
# fail across worker hardware diversity), crash dumps (sensitive), session
# state (we want clean per-session), logs.
cd "$PROFILE_DIR"
tar \
    --exclude="Default/Code Cache" \
    --exclude="Default/GPUCache" \
    --exclude="Default/Application Cache" \
    --exclude="Default/Media Cache" \
    --exclude="Default/Crash*" \
    --exclude="Default/Crashpad" \
    --exclude="Default/Sessions" \
    --exclude="Default/Session Storage" \
    --exclude="Default/blob_storage" \
    --exclude="ShaderCache" \
    --exclude="GraphiteDawnCache" \
    --exclude="GrShaderCache" \
    --exclude="*.log" \
    -czf "$OUTPUT" \
    Default \
    .cloak-fingerprint-seed \
    .cloak-fingerprint-timezone \
    .cloak-fingerprint-locale 2>/dev/null

# Report what's in the bundle
size=$(stat -c%s "$OUTPUT" 2>/dev/null || stat -f%z "$OUTPUT")
echo "[seed-profile] bundle: $OUTPUT ($(numfmt --to=iec --suffix=B "$size" 2>/dev/null || echo "${size} bytes"))"
echo "[seed-profile] contains:"
tar -tzf "$OUTPUT" | head -20 | sed 's/^/    /'
total_files=$(tar -tzf "$OUTPUT" | wc -l)
echo "    ... ($total_files files total)"

# Sanity-check the captured cookies — show count and any Akamai/auth cookies.
# Uses sqlite3 if available; otherwise just notes the cookie file is present.
if command -v sqlite3 >/dev/null 2>&1; then
    cookie_count=$(sqlite3 "$PROFILE_DIR/Default/Cookies" \
        "SELECT COUNT(*) FROM cookies" 2>/dev/null || echo "?")
    echo "[seed-profile] cookies in DB: $cookie_count"
    important=$(sqlite3 "$PROFILE_DIR/Default/Cookies" \
        "SELECT host_key || '|' || name FROM cookies WHERE name IN ('_abck','bm_sz','bm_sv','ak_bmsc','AKA_A2','dl_session_id','session') OR name LIKE 'sso%' OR name LIKE 'auth%'" 2>/dev/null | head -20)
    if [[ -n "$important" ]]; then
        echo "[seed-profile] auth/Akamai cookies present:"
        echo "$important" | sed 's/^/    /'
    else
        echo "[seed-profile] WARNING: no Akamai/auth cookies found in DB — was the seeder login successful?"
    fi
fi