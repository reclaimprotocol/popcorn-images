ARG NODE_IMAGE=docker.io/node:22-bullseye-slim@sha256:f8193a2e3e6c86d72ba378b929ae7db3f813018aff78e58cc11ac121439da8af

FROM ${NODE_IMAGE} AS downloader
WORKDIR /mirror

ARG TARGETARCH
ARG SOURCE_DATE_EPOCH=0
ARG ARTIFACT_MIRROR_PREFIX=
# Set SKIP_CHROMIUM=1 to omit chromium .deb downloads. Used by branches that
# install chromium another way (e.g. cloakbrowser) and don't need apt chromium.
ARG SKIP_CHROMIUM=
ENV SKIP_CHROMIUM=${SKIP_CHROMIUM}

COPY images/chromium-headful/chromium-lock.json /tmp/chromium-lock.json

RUN mkdir -p /mirror/artifacts/debs /mirror/artifacts/archives /mirror/artifacts/bin && \
    node <<'EOF'
const { createHash } = require('node:crypto');
const { createReadStream, createWriteStream, promises: fs } = require('node:fs');
const path = require('node:path');
const { Readable } = require('node:stream');
const { pipeline } = require('node:stream/promises');

const RETRIABLE_STATUS_CODES = new Set([408, 425, 429, 500, 502, 503, 504]);

async function sha256File(filePath) {
  const hash = createHash('sha256');
  await pipeline(createReadStream(filePath), async function* (source) {
    for await (const chunk of source) {
      hash.update(chunk);
      yield chunk;
    }
  }, createWriteStream('/dev/null'));
  return hash.digest('hex');
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function retryDelayMs(attempt, response) {
  const retryAfter = response?.headers?.get?.('retry-after');
  if (retryAfter) {
    const seconds = Number(retryAfter);
    if (Number.isFinite(seconds) && seconds >= 0) {
      return seconds * 1000;
    }
  }

  const base = Math.min(30000, 2000 * (2 ** (attempt - 1)));
  const jitter = Math.floor(Math.random() * 1000);
  return base + jitter;
}

async function fetchWithRetry(url, headers, artifactName) {
  let lastError;
  for (let attempt = 1; attempt <= 8; attempt += 1) {
    let response;
    try {
      response = await fetch(url, {
        headers,
        redirect: 'follow',
        signal: AbortSignal.timeout(1800000),
      });
      if (!response.ok) {
        if (!RETRIABLE_STATUS_CODES.has(response.status)) {
          const error = new Error(`unexpected response: ${response.status} ${response.statusText}`);
          error.retriable = false;
          throw error;
        }
        throw new Error(`unexpected response: ${response.status} ${response.statusText}`);
      }
      return response;
    } catch (error) {
      lastError = error;
      if (error?.retriable === false) {
        throw error;
      }
      if (attempt === 8) {
        throw lastError;
      }
      const delayMs = retryDelayMs(attempt, response);
      console.warn(`[download retry] ${artifactName} attempt ${attempt}/8 failed: ${error.message}; retrying in ${delayMs}ms`);
      await sleep(delayMs);
    }
  }
  throw lastError;
}

async function downloadArtifact({ url, filename, sha256, headers = {} }, outDir) {
  const outPath = path.join(outDir, filename);
  const tmpOutPath = `${outPath}.part`;
  const artifactMirrorPrefix = (process.env.ARTIFACT_MIRROR_PREFIX || '').replace(/\/+$/, '');
  const candidateUrls = [];
  if (artifactMirrorPrefix) {
    candidateUrls.push(`${artifactMirrorPrefix}/${filename}`);
  }
  candidateUrls.push(url);
  await fs.rm(tmpOutPath, { force: true });
  let lastError;

  for (const candidateUrl of candidateUrls) {
    console.log(`[download] ${filename} <- ${candidateUrl}`);

    try {
      const response = await fetchWithRetry(candidateUrl, {
        'accept': 'application/octet-stream',
        'user-agent': 'popcorn-chromium-artifact-mirror/1.0',
        ...headers,
      }, filename);

      if (!response.body) {
        throw new Error(`failed to download ${candidateUrl}: empty response body`);
      }

      try {
        await pipeline(Readable.fromWeb(response.body), createWriteStream(tmpOutPath));
        const actual = await sha256File(tmpOutPath);
        if (actual !== sha256) {
          throw new Error(`checksum mismatch for ${filename}: expected ${sha256}, got ${actual}`);
        }

        await fs.rename(tmpOutPath, outPath);
        console.log(`[download ok] ${filename}`);
        return;
      } catch (error) {
        await fs.rm(tmpOutPath, { force: true });
        throw error;
      }
    } catch (error) {
      lastError = error;
      await fs.rm(tmpOutPath, { force: true });
      if (candidateUrl !== url) {
        console.warn(`[download fallback] ${filename} mirror failed: ${error.message}`);
        continue;
      }
      throw error;
    }
  }

  throw lastError;
}

async function runWithConcurrency(items, limit, worker) {
  let nextIndex = 0;

  async function runOne() {
    while (true) {
      const currentIndex = nextIndex;
      nextIndex += 1;

      if (currentIndex >= items.length) {
        return;
      }

      await worker(items[currentIndex], currentIndex);
    }
  }

  const workers = Array.from({ length: Math.min(limit, items.length) }, () => runOne());
  await Promise.all(workers);
}

function concurrencyForHost(host) {
  if (host === 'github.com') {
    return 1;
  }
  if (host === 'launchpadlibrarian.net') {
    return 1;
  }
  if (host === 'ppa.launchpadcontent.net') {
    return 2;
  }
  if (host === 'deb.debian.org') {
    return 1;
  }
  return 2;
}

async function main() {
  const arch = process.env.TARGETARCH || 'amd64';
  const skipChromium = !!process.env.SKIP_CHROMIUM;
  const lock = JSON.parse(await fs.readFile('/tmp/chromium-lock.json', 'utf8'));
  const chromiumPackages = skipChromium ? [] : lock.chromium?.packages?.[arch];
  const libxcvtPackage = lock.libxcvt0?.packages?.[arch];
  const ffmpegArchive = lock.ffmpeg?.archives?.[arch];
  const websocatBinary = lock.websocat?.binaries?.[arch];

  if ((!skipChromium && !chromiumPackages) || !libxcvtPackage || !ffmpegArchive || !websocatBinary) {
    throw new Error(`unsupported arch in chromium lock: ${arch}`);
  }

  const downloads = [
    ...chromiumPackages.map((artifact) => ({ artifact, outDir: '/mirror/artifacts/debs' })),
    { artifact: libxcvtPackage, outDir: '/mirror/artifacts/debs' },
    { artifact: ffmpegArchive, outDir: '/mirror/artifacts/archives' },
    { artifact: websocatBinary, outDir: '/mirror/artifacts/bin' },
  ];

  const downloadsByHost = new Map();
  for (const item of downloads) {
    const host = new URL(item.artifact.url).host;
    if (!downloadsByHost.has(host)) {
      downloadsByHost.set(host, []);
    }
    downloadsByHost.get(host).push(item);
  }

  for (const [host, hostDownloads] of downloadsByHost) {
    const limit = concurrencyForHost(host);
    console.log(`[download group] host=${host} concurrency=${limit} count=${hostDownloads.length}`);
    await runWithConcurrency(hostDownloads, limit, async ({ artifact, outDir }) => {
      await downloadArtifact(artifact, outDir);
    });
  }

  const manifest = {
    arch,
    ubuntu_snapshot: lock.ubuntu_snapshot,
    chromium_version: lock.chromium.version,
    ffmpeg_version: lock.ffmpeg.version,
    websocat_version: lock.websocat.version,
    generated_by: 'artifact-mirror.Dockerfile',
  };
  await fs.writeFile('/mirror/artifacts/manifest.json', JSON.stringify(manifest, null, 2) + '\n');
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
EOF

RUN set -eux; \
    find /mirror/artifacts -exec chmod 0644 {} +; \
    find /mirror/artifacts -type d -exec chmod 0755 {} +; \
    find /mirror/artifacts -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

FROM scratch
COPY --from=downloader /mirror/artifacts/ /artifacts/
