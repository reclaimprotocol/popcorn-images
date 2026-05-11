# syntax=docker/dockerfile:1.7
# The frontend version above honors SOURCE_DATE_EPOCH for COPY mtimes; combined
# with `--output ...,rewrite-timestamp=true` on the build command, this makes the
# resulting image bit-reproducible across rebuilds from the same commit.

ARG GOLANG_IMAGE=docker.io/golang:1.26@sha256:5f3787b7f902c07c7ec4f3aa91a301a3eda8133aa32661a3b3a3a86ab3a68a36
ARG NODE_IMAGE=docker.io/node:22-bullseye-slim@sha256:f8193a2e3e6c86d72ba378b929ae7db3f813018aff78e58cc11ac121439da8af
ARG UBUNTU_IMAGE=docker.io/ubuntu:22.04@sha256:962f6cadeae0ea6284001009daa4cc9a8c37e75d1f5191cf0eb83fe565b63dd7
ARG NEKO_IMAGE=ghcr.io/kernel/neko/base:3.0.8-v1.4.0@sha256:bf8a6eddcc7b19062e77652799a35c8c50d873b73d18388cc8289f62737dd3bf
ARG ARTIFACT_MIRROR_IMAGE=docker.io/library/chromium-base-artifacts:local
# CloakBrowser is a stealth-Chromium build distributed as an OCI image. Pinned
# by digest so rebuilds get the exact same chrome+chromedriver bundle.
ARG CLOAKBROWSER_IMAGE=docker.io/cloakhq/cloakbrowser@sha256:ef8cf93374235b8432153056ad27676b39e832682589f2fb811efb5bf12c9083
ARG UBUNTU_SNAPSHOT=20260416T000000Z

FROM ${GOLANG_IMAGE} AS server-builder
WORKDIR /workspace/server

# Allow cross-compilation when building with BuildKit platforms
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-golang1260
ARG SOURCE_DATE_EPOCH=0
ENV CGO_ENABLED=1
ENV GOFLAGS=-p=2

COPY server/go.mod ./
COPY server/go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build,id=$CACHEIDPREFIX-go-build \
    --mount=type=cache,target=/go/pkg/mod,id=$CACHEIDPREFIX-go-pkg-mod \
    go mod download

COPY server/ .

# Build kernel-images API
RUN --mount=type=cache,target=/root/.cache/go-build,id=$CACHEIDPREFIX-go-build \
    --mount=type=cache,target=/go/pkg/mod,id=$CACHEIDPREFIX-go-pkg-mod \
    GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" -o /out/kernel-images-api ./cmd/api

# Build chromium launcher
RUN --mount=type=cache,target=/root/.cache/go-build,id=$CACHEIDPREFIX-go-build \
    --mount=type=cache,target=/go/pkg/mod,id=$CACHEIDPREFIX-go-pkg-mod \
    GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" -o /out/chromium-launcher ./cmd/chromium-launcher

# webrtc client
FROM ${NODE_IMAGE} AS client
WORKDIR /src

# Allow cross-compilation when building with BuildKit platforms
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-node22bullseye

COPY images/chromium-headful/client/package*.json ./
RUN --mount=type=cache,target=/root/.npm,id=$CACHEIDPREFIX-npm npm ci
COPY images/chromium-headful/client/ .
RUN --mount=type=cache,target=/root/.npm,id=$CACHEIDPREFIX-npm npm run build

FROM ${UBUNTU_IMAGE} AS ubuntu-snapshot
# UBUNTU_SNAPSHOT is kept as an ARG for compatibility with the parent CI, which
# still passes it via --build-arg. We don't actually rewrite apt sources to
# snapshot.ubuntu.com here because that cluster (185.125.189.0/24) filters
# Blacksmith and most cloud/CI egress IP ranges, leaving CI permanently broken.
# Falling back to the base image's default archive.ubuntu.com sources trades
# cross-day apt reproducibility for build reachability — same-host-same-day
# reproducibility within minutes still holds, and specific packages we care
# about being pinned (libxcvt0, ffmpeg, websocat) are still SHA-pinned via
# chromium-lock.json + the artifact-mirror flow.
#
# We also redirect security.ubuntu.com → archive.ubuntu.com because Canonical's
# security mirror (185.125.190.0/24) is in the same egress-filtered range as
# snapshot.ubuntu.com and is unreachable from Blacksmith. archive.ubuntu.com
# serves the security pocket too with a small replication delay, which is
# acceptable for jammy LTS.
ARG UBUNTU_SNAPSHOT=
ARG SOURCE_DATE_EPOCH=0
RUN --mount=from=server-builder,source=/etc/ssl/certs/ca-certificates.crt,target=/tmp/ca-certificates.crt,ro \
    set -eux; \
    mkdir -p /etc/ssl/certs; \
    install -m0644 /tmp/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt; \
    sed -i \
        -e 's|http://security\.ubuntu\.com/ubuntu|http://archive.ubuntu.com/ubuntu|g' \
        -e 's|http://[a-z]*\.archive\.ubuntu\.com/ubuntu|http://archive.ubuntu.com/ubuntu|g' \
        /etc/apt/sources.list; \
    touch -h -d "@${SOURCE_DATE_EPOCH}" \
        /etc \
        /etc/ssl \
        /etc/ssl/certs \
        /etc/ssl/certs/ca-certificates.crt \
        /etc/apt \
        /etc/apt/sources.list

# xorg dependencies
FROM ubuntu-snapshot AS xorg-deps
WORKDIR /xorg

# Allow cross-compilation when building with BuildKit platforms
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-ubuntu2204
ARG SOURCE_DATE_EPOCH=0
ENV DEBIAN_FRONTEND=noninteractive
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-xorg-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-xorg-apt-lib \
    set -eux; \
    rm -f /etc/apt/apt.conf.d/docker-clean; \
    echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache; \
    apt-get update; \
    apt-get --no-install-recommends -y install \
    git gcc pkgconf autoconf automake libtool make xorg-dev xutils-dev;
COPY images/chromium-headful/xorg-deps/ /xorg/
# build xf86-video-dummy v0.3.8 with RandR support
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-xorg-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-xorg-apt-lib \
    set -eux; \
    cd xf86-video-dummy/v0.3.8; \
    patch -p1 < ../01_v0.3.8_xdummy-randr.patch; \
    autoreconf -v --install; \
    ./configure; \
    make -j$(nproc); \
    make install;
# build custom input driver
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-xorg-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-xorg-apt-lib \
    set -eux; \
    cd xf86-input-neko; \
    ./autogen.sh --prefix=/usr; \
    ./configure; \
    make -j$(nproc); \
    make install;

FROM ${NEKO_IMAGE} AS neko
# ^--- now has event.SYSTEM_PONG with legacy support to keepalive
FROM ${NODE_IMAGE} AS node-22
FROM ${ARTIFACT_MIRROR_IMAGE} AS artifact-mirror
FROM ${CLOAKBROWSER_IMAGE} AS cloakbrowser
FROM ubuntu-snapshot AS runtime-base

# Allow cross-compilation when building with BuildKit platforms
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-ubuntu2204
ARG SOURCE_DATE_EPOCH=0
ARG TYPESCRIPT_VERSION=6.0.2
ARG PLAYWRIGHT_CORE_VERSION=1.59.1
ARG PATCHRIGHT_VERSION=1.59.4
ARG ESBUILD_VERSION=0.28.0
ARG CLOAKBROWSER_NPM_VERSION=0.3.26

ENV DEBIAN_FRONTEND=noninteractive
ENV DEBIAN_PRIORITY=high

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-lib \
    --mount=from=artifact-mirror,source=/artifacts,target=/mnt/artifacts,ro \
    set -eux; \
    touch_start="$(date -u +'%Y-%m-%d %H:%M:%S')"; \
    rm -f /etc/apt/apt.conf.d/docker-clean; \
    echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache; \
    mkdir -p /tmp/artifacts; \
    cp -a /mnt/artifacts/. /tmp/artifacts/; \
    apt-get update && \
    apt-get --no-install-recommends -y install \
    gpg-agent \
    xvfb \
    xterm \
    xdotool \
    scrot \
    imagemagick \
    sudo \
    mutter \
    build-essential \
    libssl-dev \
    zlib1g-dev \
    libbz2-dev \
    libreadline-dev \
    libsqlite3-dev \
    curl \
    git \
    libncursesw5-dev \
    xz-utils \
    tk-dev \
    libxml2-dev \
    libxmlsec1-dev \
    libffi-dev \
    liblzma-dev \
    net-tools \
    x11-apps \
    tint2 \
    wget \
    xdg-utils \
    libvulkan1 \
    fontconfig \
    sqlite3 \
    # Chromium runtime libs — needed by cloakbrowser since we don't apt-install chromium.
    libcups2 libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 \
    libasound2 libpango-1.0-0 libxdamage1 libxkbcommon0 libdrm2 \
    libxcomposite1 libxfixes3 libxrandr2 libgbm1 \
    # libxcvt0 from artifact-mirror (debian, not in jammy). Glob excludes chromium*.deb;
    # those are downloaded into /tmp/artifacts/debs alongside libxcvt0 but unused
    # because cloakbrowser provides chrome+chromedriver.
    /tmp/artifacts/debs/libxcvt0_*.deb \
    unzip && \
    tar -xJf /tmp/artifacts/archives/*.tar.xz -C /tmp && \
    install -m755 /tmp/ffmpeg-*/bin/ffmpeg /usr/local/bin/ffmpeg && \
    install -m755 /tmp/ffmpeg-*/bin/ffprobe /usr/local/bin/ffprobe && \
    install -m0755 /tmp/artifacts/bin/websocat.* /usr/local/bin/websocat && \
    rm -rf /tmp/ffmpeg* /tmp/artifacts && \
    rm -f /etc/machine-id /var/lib/dbus/machine-id /var/cache/ldconfig/aux-cache \
        /run/systemd/resolve/stub-resolv.conf && \
    rm -rf /var/cache/debconf/* && \
    find /usr/local/share/fonts /usr/share/fonts -name '.uuid' -delete 2>/dev/null || true; \
    rm -rf /var/cache/fontconfig/* && \
    find /var/log -mindepth 1 -delete && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && \
    find /etc /mnt /run /tmp /usr /var -xdev \
        \( -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf -o -path /mnt/artifacts -o -path /mnt/artifacts/\* \) -prune -o \
        -newerct "$touch_start" \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch AS runtime-checkpoint-1
COPY --from=runtime-base / /

# Install CloakBrowser (stealth Chromium build) in place of stock chromium.
# The image bundles a matching chromedriver next to the chrome binary for both
# amd64 and arm64, so we skip the per-arch chromedriver dance.
FROM runtime-base AS runtime-chrome
ARG SOURCE_DATE_EPOCH=0
COPY --from=cloakbrowser /root/.cloakbrowser /opt/cloakbrowser
RUN set -eux; \
    touch_start="$(date -u +'%Y-%m-%d %H:%M:%S')"; \
    CLOAK_DIR=$(find /opt/cloakbrowser -maxdepth 1 -type d -name 'chromium-*' | head -n1); \
    test -n "$CLOAK_DIR"; \
    test -x "$CLOAK_DIR/chrome"; \
    test -x "$CLOAK_DIR/chromedriver"; \
    ln -sf "$CLOAK_DIR/chrome" /usr/bin/chromium; \
    ln -sf "$CLOAK_DIR/chrome" /usr/bin/chromium-browser; \
    ln -sf "$CLOAK_DIR/chromedriver" /usr/local/bin/chromedriver; \
    chromium --version; \
    chromedriver --version; \
    find /etc /opt /usr -xdev \
        \( -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf \) -prune -o \
        -newerct "$touch_start" \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch AS runtime-checkpoint-1b
COPY --from=runtime-chrome / /

FROM runtime-chrome AS runtime-fonts
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-ubuntu2204
ARG SOURCE_DATE_EPOCH=0
# Install fonts to match a realistic Ubuntu 22.04 desktop fingerprint.
# A minimal container has only 3 fonts, which is a strong fingerprinting signal
# used by reCAPTCHA Enterprise and other bot detection vendors.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-lib \
    set -eux; \
    touch_start="$(date -u +'%Y-%m-%d %H:%M:%S')"; \
    apt-get update && \
    apt-get --no-install-recommends -y install \
    fonts-liberation \
    fonts-liberation2 \
    fonts-noto-cjk \
    fonts-noto-color-emoji \
    fonts-noto-core \
    fonts-nanum \
    fonts-dejavu-core \
    fonts-dejavu-extra \
    fonts-freefont-ttf \
    fonts-ubuntu \
    fonts-opensymbol \
    fonts-roboto \
    fonts-croscore \
    fonts-smc-chilanka \
    fonts-hosny-amiri \
    fonts-indic \
    fonts-kacst \
    fonts-khmeros-core \
    fonts-lao \
    fonts-lklug-sinhala \
    fonts-sil-abyssinica \
    fonts-sil-padauk \
    fonts-thai-tlwg \
    fonts-tibetan-machine \
    fonts-droid-fallback \
    # Anti-bot canvas-hash coverage: Kasada/Akamai render emoji and CJK glyphs
    # on a hidden canvas and hash the pixel output. Missing any of these
    # produces tofu boxes that hash differently from real Chrome on a real
    # OS. fonts-noto-color-emoji + fonts-freefont-ttf are already above;
    # the four below close the remaining gaps (Unifont fallback, JP, CN,
    # Thai/Lao). Per CloakBrowser docs.
    fonts-unifont \
    fonts-ipafont-gothic \
    fonts-wqy-zenhei \
    fonts-tlwg-loma-otf && \
    fc-cache -f && \
    find /usr/local/share/fonts /usr/share/fonts -name '.uuid' -delete 2>/dev/null || true; \
    rm -rf /var/cache/debconf/* && \
    rm -rf /var/cache/fontconfig/* && \
    find /var/log -mindepth 1 -delete && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && \
    find /etc /mnt /run /tmp /usr /var -xdev \
        \( -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf -o -path /mnt/artifacts -o -path /mnt/artifacts/\* \) -prune -o \
        -newerct "$touch_start" \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch AS runtime-checkpoint-2
COPY --from=runtime-fonts / /

FROM runtime-fonts AS runtime-system
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-ubuntu2204
ARG SOURCE_DATE_EPOCH=0
# runtime
ENV USERNAME=root
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-lib \
    set -eux; \
    touch_start="$(date -u +'%Y-%m-%d %H:%M:%S')"; \
    apt-get update; \
    apt-get --no-install-recommends -y install \
    wget ca-certificates python2 supervisor xclip xdotool unclutter \
    pulseaudio dbus-x11 xserver-xorg-video-dummy \
    libcairo2 libxcb1 libxrandr2 libxv1 libopus0 libvpx7 \
    x11-xserver-utils \
    gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly \
    gstreamer1.0-pulseaudio gstreamer1.0-omx; \
    #
    # workaround for an X11 problem: http://blog.tigerteufel.de/?p=476
    mkdir /tmp/.X11-unix; \
    chmod 1777 /tmp/.X11-unix; \
    chown $USERNAME /tmp/.X11-unix/; \
    #
    # make directories for neko
    mkdir -p /etc/neko /var/www /var/log/neko \
    /tmp/runtime-$USERNAME \
    /home/$USERNAME/.config/pulse  \
    /home/$USERNAME/.local/share/xorg; \
    chmod 1777 /var/log/neko; \
    chown $USERNAME /var/log/neko/ /tmp/runtime-$USERNAME; \
    chown -R $USERNAME:$USERNAME /home/$USERNAME; \
    rm -f /etc/machine-id /var/lib/dbus/machine-id /var/cache/ldconfig/aux-cache; \
    rm -rf /var/cache/debconf/*; \
    find /var/log -mindepth 1 \
        ! -path /var/log/neko \
        ! -path /var/log/neko/* \
        -delete; \
    find /etc /home /mnt /run /tmp /usr /var -xdev \
        \( -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf -o -path /mnt/artifacts -o -path /mnt/artifacts/\* \) -prune -o \
        -newerct "$touch_start" \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch AS runtime-checkpoint-3
COPY --from=runtime-system / /

FROM runtime-system AS runtime-node
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-ubuntu2204
ARG SOURCE_DATE_EPOCH=0
ARG TYPESCRIPT_VERSION=6.0.2
ARG PLAYWRIGHT_CORE_VERSION=1.59.1
ARG PATCHRIGHT_VERSION=1.59.4
ARG ESBUILD_VERSION=0.28.0
ARG CLOAKBROWSER_NPM_VERSION=0.3.26
# Copy Chromium policy configuration
RUN mkdir -p /etc/chromium/policies/managed
COPY shared/chromium-policies/managed/policy.json /etc/chromium/policies/managed/policy.json

# install Node.js 22.x by copying from the node:22-bullseye-slim stage
COPY --from=node-22 /usr/local/bin/node /usr/local/bin/node
COPY --from=node-22 /usr/local/lib/node_modules /usr/local/lib/node_modules
# Recreate symlinks for npm/npx/corepack to point into node_modules
RUN set -eux; \
    ln -sf /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm; \
    ln -sf /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx; \
    if [ -e /usr/local/lib/node_modules/corepack/dist/corepack.js ]; then \
    ln -sf /usr/local/lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack; \
    fi

# Install TypeScript, Playwright, Patchright, CloakBrowser (JS humanize helpers
# only — the chromium binary already comes from the cloakbrowser OCI image
# stage), and esbuild globally.
#
# CLOAKBROWSER_AUTO_UPDATE=false + CLOAKBROWSER_BINARY_PATH point the npm
# package at the existing /opt/cloakbrowser binary so its postinstall doesn't
# fetch a duplicate ~200MB chromium at image-build time. We only need the JS
# package for `cloakbrowser/human` (patchBrowser, resolveConfig) — the daemon
# never calls launch().
ENV CLOAKBROWSER_AUTO_UPDATE=false
ENV CLOAKBROWSER_BINARY_PATH=/usr/bin/chromium
RUN --mount=type=cache,target=/root/.npm,id=$CACHEIDPREFIX-npm \
    set -eux; \
    touch_start="$(date -u +'%Y-%m-%d %H:%M:%S')"; \
    npm install -g \
        typescript@${TYPESCRIPT_VERSION} \
        playwright-core@${PLAYWRIGHT_CORE_VERSION} \
        patchright@${PATCHRIGHT_VERSION} \
        cloakbrowser@${CLOAKBROWSER_NPM_VERSION} \
        esbuild@${ESBUILD_VERSION}; \
    rm -rf /tmp/node-compile-cache; \
    find /etc /home /mnt /root /run /tmp /usr /var -xdev \
        \( -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf -o -path /mnt/artifacts -o -path /mnt/artifacts/\* -o -path /root/.npm -o -path /root/.npm/\* \) -prune -o \
        -newerct "$touch_start" \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch AS runtime-checkpoint-4
COPY --from=runtime-node / /

FROM runtime-node AS runtime-ssh
ARG TARGETARCH
ARG TARGETOS
ARG CACHEIDPREFIX=${TARGETOS:-linux}-${TARGETARCH:-amd64}-ubuntu2204
ARG SOURCE_DATE_EPOCH=0
# Pre-install openssh-server and websocat for faster SSH setup via kernel CLI.
# Only installed, NOT configured: no host keys, no custom config, service not enabled.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-cache \
    --mount=type=cache,target=/var/lib/apt,sharing=locked,id=$CACHEIDPREFIX-runtime-apt-lib \
    set -eux; \
    touch_start="$(date -u +'%Y-%m-%d %H:%M:%S')"; \
    apt-get update && \
    apt-get --no-install-recommends -y install openssh-server && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && \
    rm -f /var/cache/ldconfig/aux-cache && \
    rm -rf /var/cache/debconf/* && \
    rm -f /etc/ssh/ssh_host_* && \
    find /var/log -mindepth 1 \
        ! -path /var/log/neko \
        ! -path /var/log/neko/* \
        -delete && \
    find /etc /home /mnt /run /tmp /usr /var -xdev \
        \( -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf -o -path /mnt/artifacts -o -path /mnt/artifacts/\* \) -prune -o \
        -newerct "$touch_start" \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch AS runtime-checkpoint-5
COPY --from=runtime-ssh / /

FROM runtime-ssh AS runtime
ARG SOURCE_DATE_EPOCH=0
# setup desktop env & app
ENV DISPLAY_NUM=1
ENV HEIGHT=1080
ENV WIDTH=1920
ENV WITHDOCKER=true

COPY images/chromium-headful/xorg.conf /etc/neko/xorg.conf
COPY images/chromium-headful/neko.yaml /etc/neko/neko.yaml
COPY --from=neko /usr/bin/neko /usr/bin/neko
COPY --from=client /src/dist/ /var/www
COPY --from=xorg-deps /usr/local/lib/xorg/modules/drivers/dummy_drv.so /usr/lib/xorg/modules/drivers/dummy_drv.so
COPY --from=xorg-deps /usr/local/lib/xorg/modules/input/neko_drv.so /usr/lib/xorg/modules/input/neko_drv.so

COPY images/chromium-headful/image-chromium/ /
COPY --chmod=0755 images/chromium-headful/start-pulseaudio.sh /images/chromium-headful/start-pulseaudio.sh
COPY --chmod=0755 images/chromium-headful/wrapper.sh /wrapper.sh
COPY images/chromium-headful/supervisord.conf /etc/supervisor/supervisord.conf
COPY images/chromium-headful/supervisor/services/ /etc/supervisor/conf.d/services/
# copy the kernel-images API binary built in the builder stage
COPY --from=server-builder /out/kernel-images-api /usr/local/bin/kernel-images-api
COPY --from=server-builder /out/chromium-launcher /usr/local/bin/chromium-launcher

# Copy bundled extensions
COPY extensions/proxy/ /home/kernel/extensions/proxy/

# Seeder helper: captures a warm chromium profile after a successful login
# for use as CLOAK_PROFILE_SEED in worker pods. Run from a seeder pod after
# completing a supervised login in the live browser:
#   docker exec <seeder> /scripts/seed-profile.sh /tmp/profile-state.tar.gz
COPY --chmod=0755 images/chromium-headful/scripts/seed-profile.sh /scripts/seed-profile.sh

# Copy and compile the Playwright daemon
COPY server/runtime/playwright-daemon.ts /tmp/playwright-daemon.ts
RUN esbuild /tmp/playwright-daemon.ts \
    --bundle \
    --platform=node \
    --target=node22 \
    --format=esm \
    --outfile=/usr/local/lib/playwright-daemon.js \
    --external:playwright-core \
    --external:patchright \
    --external:cloakbrowser \
    --external:esbuild \
    && rm /tmp/playwright-daemon.ts

RUN useradd -m -s /bin/bash kernel && \
    chage -d "$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%d)" kernel

FROM runtime AS packager
ARG SOURCE_DATE_EPOCH=0
RUN set -eux; \
    rm -f /etc/machine-id /var/lib/dbus/machine-id /var/cache/ldconfig/aux-cache \
        /run/systemd/resolve/stub-resolv.conf; \
    rm -rf /var/cache/debconf/*; \
    rm -rf /var/cache/fontconfig/*; \
    find /usr/local/share/fonts /usr/share/fonts -name '.uuid' -delete 2>/dev/null || true; \
    find /var/log -mindepth 1 \
        ! -path /var/log/neko \
        ! -path /var/log/neko/* \
        -delete; \
    mkdir -p /var/log/neko; \
    chmod 1777 /var/log/neko; \
    find / \
        \( -path /proc -o -path /sys -o -path /dev -o -path /etc/hosts -o -path /etc/hostname -o -path /etc/resolv.conf \) -prune -o \
        -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENV USERNAME=root
ENV DISPLAY_NUM=1
ENV HEIGHT=1080
ENV WIDTH=1920
ENV WITHDOCKER=true
COPY --from=packager / /

ENTRYPOINT [ "/wrapper.sh" ]
