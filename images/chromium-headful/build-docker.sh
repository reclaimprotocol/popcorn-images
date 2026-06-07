#!/usr/bin/env bash
set -e -o pipefail

# Move to the script's directory so relative paths work regardless of caller CWD
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
cd "$SCRIPT_DIR"
source ../../shared/ensure-common-build-run-vars.sh chromium-headful

source ../../shared/start-buildkit.sh

REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
PLATFORM="${PLATFORM:-linux/amd64}"
TARGET_PLATFORM="$PLATFORM"

# UBUNTU_SNAPSHOT is the only build-arg the main Dockerfile reads from the
# chromium lock file. Pull it via awk so we don't depend on jq/node on the host.
UBUNTU_SNAPSHOT=$(awk -F'"' '/"ubuntu_snapshot"/ {print $4; exit}' "$SCRIPT_DIR/chromium-lock.json")
if [[ -z "$UBUNTU_SNAPSHOT" ]]; then
    echo "Failed to read ubuntu_snapshot from chromium-lock.json" >&2
    exit 1
fi

# FAST_BUILD=1 pins SOURCE_DATE_EPOCH to a constant so the timestamp-derived
# layers stop invalidating on every commit. Combine with BUILD_CACHE_DIR for
# layer reuse across runs — the two together turn a clean rebuild into a
# cache-hit walk for everything that hasn't actually changed.
if [[ "$FAST_BUILD" == "1" ]]; then
    SOURCE_DATE_EPOCH=0
    echo "FAST_BUILD=1: SOURCE_DATE_EPOCH=0 for layer reuse."
else
    SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$REPO_ROOT" log -1 --pretty=%ct)}"
fi

# Persist BuildKit's internal cache to disk when BUILD_CACHE_DIR is set.
# Survives daemon restarts and cleans pulls of the dev image.
CACHE_ARGS=()
if [[ -n "$BUILD_CACHE_DIR" ]]; then
    mkdir -p "$BUILD_CACHE_DIR"
    CACHE_ARGS=(--cache-from "type=local,src=$BUILD_CACHE_DIR" --cache-to "type=local,dest=$BUILD_CACHE_DIR,mode=max")
    echo "Using local build cache: $BUILD_CACHE_DIR"
fi

# Build (or reuse) the artifact-mirror image. This produces a scratch image
# containing /artifacts/{debs,archives,bin}, which the main Dockerfile mounts
# read-only via `--mount=from=artifact-mirror,source=/artifacts,...`. The image
# is pinned by chromium-lock.json, so re-runs are cheap once the layer is
# cached. Rebuild it explicitly when the lock file changes.
ARTIFACT_MIRROR_IMAGE="${ARTIFACT_MIRROR_IMAGE:-chromium-base-artifacts:local}"

# SKIP_CHROMIUM=1 omits the chromium .deb downloads from the artifact-mirror.
# The chromium .debs in chromium-lock.json get evicted from launchpadlibrarian
# whenever xtradeb pushes a new version (currently 148.x; lock pins 147.x),
# so the downloader 404s. The main Dockerfile doesn't actually install those
# debs anyway — only libxcvt0/ffmpeg/websocat from the mirror are used; the
# chromium binary itself comes from cloakbrowser at runtime. So defaulting
# SKIP_CHROMIUM=1 unblocks builds with no functional change.
SKIP_CHROMIUM="${SKIP_CHROMIUM:-1}"

(cd "$REPO_ROOT" && docker buildx build \
    --platform "$TARGET_PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    --build-arg "SKIP_CHROMIUM=$SKIP_CHROMIUM" \
    "${CACHE_ARGS[@]}" \
    -f images/chromium-headful/artifact-mirror.Dockerfile \
    -t "$ARTIFACT_MIRROR_IMAGE" \
    --load \
    .)

(cd "$REPO_ROOT" && docker buildx build \
    --platform "$TARGET_PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    --build-arg "UBUNTU_SNAPSHOT=$UBUNTU_SNAPSHOT" \
    --build-arg "ARTIFACT_MIRROR_IMAGE=$ARTIFACT_MIRROR_IMAGE" \
    "${CACHE_ARGS[@]}" \
    -f images/chromium-headful/Dockerfile \
    -t "$IMAGE" \
    --load \
    .)
echo "Built image: $IMAGE (SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH)"
