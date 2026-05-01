#!/usr/bin/env bash
set -e -o pipefail

# Move to the script's directory so relative paths work regardless of the caller CWD
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
cd "$SCRIPT_DIR"
source ../../shared/ensure-common-build-run-vars.sh chromium-headful

source ../../shared/start-buildkit.sh

# Resolve UBUNTU_SNAPSHOT and ARTIFACT_MIRROR_TAG. Prefer the parent popcorn
# repo's helper if present (provides extra metadata for CI mirroring); fall
# back to reading the lock file directly so this script works standalone.
SUBMODULE_REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
PARENT_HELPER="$SCRIPT_DIR/../../../scripts/chromium-lock-env.sh"
LOCK_FILE="$SCRIPT_DIR/chromium-lock.json"
if [ -f "$PARENT_HELPER" ]; then
    PARENT_REPO_ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)
    eval "$("$PARENT_HELPER" "${PLATFORM:-}")"
else
    PARENT_REPO_ROOT="$SUBMODULE_REPO_ROOT"
    UBUNTU_SNAPSHOT=$(python3 -c "import json;print(json.load(open('$LOCK_FILE'))['ubuntu_snapshot'])")
    ARTIFACT_MIRROR_TAG="${ARTIFACT_MIRROR_TAG:-local}"
fi

# SOURCE_DATE_EPOCH pins layer mtimes and the image config's `created` field.
# Combined with `rewrite-timestamp=true` on the buildx output, this makes the
# image bit-reproducible across rebuilds from the same commit.
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$SUBMODULE_REPO_ROOT" log -1 --pretty=%ct)}"
ARTIFACT_MIRROR_IMAGE="${ARTIFACT_MIRROR_IMAGE:-chromium-base-artifacts:${ARTIFACT_MIRROR_TAG}}"
ARTIFACT_MIRROR_PREFIX="${ARTIFACT_MIRROR_PREFIX:-}"

if [[ -z "$ARTIFACT_MIRROR_PREFIX" && -n "${GITHUB_ARTIFACT_MIRROR_REPO:-}" ]]; then
    ARTIFACT_MIRROR_PREFIX="https://github.com/${GITHUB_ARTIFACT_MIRROR_REPO}/releases/download/${ARTIFACT_MIRROR_TAG}"
fi

# Force linux/amd64 for both stages. Cross-day-pinned package versions in the
# lock file are only guaranteed available for amd64 (arm64 PPA artifacts may
# rotate independently). Override with PLATFORM env if you really need arm64.
PLATFORM="${PLATFORM:-linux/amd64}"

# Build the artifact-mirror image first (chromium-lock-pinned debs, ffmpeg, websocat).
# Uses the default docker driver so the resulting image lands in the local daemon
# where the main build (also docker driver) can resolve it via FROM.
# We only consume libxcvt0.deb + ffmpeg + websocat from it; the chromium .debs are
# unused — cloakbrowser provides chrome/chromedriver — so SKIP_CHROMIUM=1.
(cd "$PARENT_REPO_ROOT" && docker buildx build \
    --platform="$PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=0" \
    --build-arg "ARTIFACT_MIRROR_PREFIX=$ARTIFACT_MIRROR_PREFIX" \
    --build-arg "SKIP_CHROMIUM=1" \
    --provenance=false --sbom=false \
    -f "$SCRIPT_DIR/artifact-mirror.Dockerfile" \
    -t "$ARTIFACT_MIRROR_IMAGE" \
    --load \
    "$SUBMODULE_REPO_ROOT")

# Main image build. SOURCE_DATE_EPOCH + UBUNTU_SNAPSHOT pin contents to the
# commit; the inline `find -newerct -exec touch -h -d "@${SOURCE_DATE_EPOCH}"`
# patterns in each stage normalize mtimes, and the final `FROM scratch + COPY
# --from=packager / /` flatten produces a single-layer image. Reproducibility
# is then verifiable by comparing the image config sha256 across rebuilds.
(cd "$SUBMODULE_REPO_ROOT" && docker buildx build \
    --platform="$PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    --build-arg "UBUNTU_SNAPSHOT=$UBUNTU_SNAPSHOT" \
    --build-arg "ARTIFACT_MIRROR_IMAGE=$ARTIFACT_MIRROR_IMAGE" \
    --provenance=false --sbom=false \
    -f images/chromium-headful/Dockerfile \
    -t "$IMAGE" \
    --load \
    .)
echo "Built image: $IMAGE (SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH)"
