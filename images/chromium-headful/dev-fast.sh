#!/usr/bin/env bash
# Fast iterate: cached docker build, then run as root.
# - FAST_BUILD=1 pins SOURCE_DATE_EPOCH so timestamp-keyed layers stay cached.
# - BUILD_CACHE_DIR persists BuildKit's cache across runs.
# - RUN_AS_ROOT=true is the only configuration this script forces; everything
#   else (PLATFORM, IMAGE, ports, etc.) flows through to the underlying
#   build/run scripts via env.
set -e -o pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

export FAST_BUILD="${FAST_BUILD:-1}"
export BUILD_CACHE_DIR="${BUILD_CACHE_DIR:-$HOME/.cache/popcorn-buildkit}"
export RUN_AS_ROOT="${RUN_AS_ROOT:-true}"
export WITH_KERNEL_IMAGES_API="${WITH_KERNEL_IMAGES_API:-true}"
export ENABLE_WEBRTC="${ENABLE_WEBRTC:-true}"

IMAGE_TYPE=chromium-headful
IMAGE="${IMAGE:-onkernel/${IMAGE_TYPE}-test:latest}"

# Only build when the image is missing or BUILD=1 is set. Set BUILD=1 to
# force a rebuild after code changes.
if [[ "$BUILD" == "1" ]] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "==> build (FAST_BUILD=$FAST_BUILD, BUILD_CACHE_DIR=$BUILD_CACHE_DIR)"
    "$SCRIPT_DIR/build-docker.sh"
else
    echo "==> skipping build (image $IMAGE present; BUILD=1 to force)"
fi

echo "==> run (RUN_AS_ROOT=$RUN_AS_ROOT, WITH_KERNEL_IMAGES_API=$WITH_KERNEL_IMAGES_API, ENABLE_WEBRTC=$ENABLE_WEBRTC)"
exec "$SCRIPT_DIR/run-docker.sh" "$@"
