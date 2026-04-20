ARG NODE_IMAGE=docker.io/node:22-bullseye-slim@sha256:f8193a2e3e6c86d72ba378b929ae7db3f813018aff78e58cc11ac121439da8af

FROM ${NODE_IMAGE} AS downloader
WORKDIR /mirror

ARG TARGETARCH
ARG SOURCE_DATE_EPOCH=0

COPY images/chromium-headful/chromium-lock.json /tmp/chromium-lock.json

RUN mkdir -p /mirror/artifacts/debs /mirror/artifacts/archives /mirror/artifacts/bin && \
    node <<'EOF'
const { createHash } = require('node:crypto');
const { createReadStream, createWriteStream, promises: fs } = require('node:fs');
const path = require('node:path');
const { Readable } = require('node:stream');
const { pipeline } = require('node:stream/promises');

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

async function fetchWithRetry(url, headers) {
  let lastError;
  for (let attempt = 1; attempt <= 5; attempt += 1) {
    try {
      const response = await fetch(url, {
        headers,
        redirect: 'follow',
        signal: AbortSignal.timeout(1800000),
      });
      if (!response.ok) {
        throw new Error(`unexpected response: ${response.status} ${response.statusText}`);
      }
      return response;
    } catch (error) {
      lastError = error;
      if (attempt === 5) {
        throw lastError;
      }
      await sleep(attempt * 2000);
    }
  }
  throw lastError;
}

async function downloadArtifact({ url, filename, sha256, headers = {} }, outDir) {
  const outPath = path.join(outDir, filename);
  const response = await fetchWithRetry(url, {
    'accept': 'application/octet-stream',
    'user-agent': 'popcorn-chromium-artifact-mirror/1.0',
    ...headers,
  });

  if (!response.body) {
    throw new Error(`failed to download ${url}: empty response body`);
  }

  await pipeline(Readable.fromWeb(response.body), createWriteStream(outPath));
  const actual = await sha256File(outPath);
  if (actual !== sha256) {
    throw new Error(`checksum mismatch for ${filename}: expected ${sha256}, got ${actual}`);
  }
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

async function main() {
  const arch = process.env.TARGETARCH || 'amd64';
  const lock = JSON.parse(await fs.readFile('/tmp/chromium-lock.json', 'utf8'));
  const chromiumPackages = lock.chromium?.packages?.[arch];
  const libxcvtPackage = lock.libxcvt0?.packages?.[arch];
  const ffmpegArchive = lock.ffmpeg?.archives?.[arch];
  const websocatBinary = lock.websocat?.binaries?.[arch];

  if (!chromiumPackages || !libxcvtPackage || !ffmpegArchive || !websocatBinary) {
    throw new Error(`unsupported arch in chromium lock: ${arch}`);
  }

  const downloads = [
    ...chromiumPackages.map((artifact) => ({ artifact, outDir: '/mirror/artifacts/debs' })),
    { artifact: libxcvtPackage, outDir: '/mirror/artifacts/debs' },
    { artifact: ffmpegArchive, outDir: '/mirror/artifacts/archives' },
    { artifact: websocatBinary, outDir: '/mirror/artifacts/bin' },
  ];

  await runWithConcurrency(downloads, 4, async ({ artifact, outDir }) => {
    await downloadArtifact(artifact, outDir);
  });

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
