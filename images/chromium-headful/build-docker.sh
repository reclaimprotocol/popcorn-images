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

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$REPO_ROOT" log -1 --pretty=%ct)}"

# Build (or reuse) the artifact-mirror image. This produces a scratch image
# containing /artifacts/{debs,archives,bin}, which the main Dockerfile mounts
# read-only via `--mount=from=artifact-mirror,source=/artifacts,...`. The image
# is pinned by chromium-lock.json, so re-runs are cheap once the layer is
# cached. Rebuild it explicitly when the lock file changes.
ARTIFACT_MIRROR_IMAGE="${ARTIFACT_MIRROR_IMAGE:-chromium-base-artifacts:local}"

(cd "$REPO_ROOT" && docker buildx build \
    --platform "$TARGET_PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    -f images/chromium-headful/artifact-mirror.Dockerfile \
    -t "$ARTIFACT_MIRROR_IMAGE" \
    --load \
    .)

(cd "$REPO_ROOT" && docker buildx build \
    --platform "$TARGET_PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    --build-arg "UBUNTU_SNAPSHOT=$UBUNTU_SNAPSHOT" \
    --build-arg "ARTIFACT_MIRROR_IMAGE=$ARTIFACT_MIRROR_IMAGE" \
    -f images/chromium-headful/Dockerfile \
    -t "$IMAGE" \
    --load \
    .)
echo "Built image: $IMAGE (SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH)"
