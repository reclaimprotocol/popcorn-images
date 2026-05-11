// Popcorn Proxy Extension - Background Service Worker (MV3)
// Sets proxy only - auth handled via CDP Fetch

const TRUSTED_ORIGINS_STORAGE_KEY = 'pcnTrustedOrigins';

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (!message || typeof message.type !== 'string' || !message.type.startsWith('PCN_PROXY_')) {
    return;
  }

  (async () => {
    try {
      await validateProxyCaller(sender, message.requestOrigin || sender?.tab?.url);

      if (message.type === 'PCN_PROXY_SET') {
        const result = await handleSetProxy(message.config);
        sendResponse({ success: true, result });
        return;
      }

      if (message.type === 'PCN_PROXY_CLEAR') {
        const result = await handleClearProxy();
        sendResponse({ success: true, result });
        return;
      }

      if (message.type === 'PCN_PROXY_GET') {
        chrome.storage.local.get(['proxyConfig'], (result) => {
          sendResponse({ success: true, config: result.proxyConfig || null });
        });
      }
    } catch (error) {
      sendResponse({ success: false, error: error.message });
    }
  })();

  return true;
});

async function validateProxyCaller(sender, requestOrigin) {
  const tabUrl = sender?.tab?.url || sender?.url;
  const messageOrigin = requestOrigin || tabUrl;
  const senderOrigin = tabUrl ? getUrlOrigin(tabUrl) : null;
  const requesterOrigin = messageOrigin ? getUrlOrigin(messageOrigin) : null;

  if (!senderOrigin || !requesterOrigin || senderOrigin !== requesterOrigin) {
    throw new Error('Untrusted proxy request origin');
  }

  const config = await chrome.storage.local.get([TRUSTED_ORIGINS_STORAGE_KEY]);
  const trustedOrigins = config[TRUSTED_ORIGINS_STORAGE_KEY];

  if (!Array.isArray(trustedOrigins) || trustedOrigins.length === 0) {
    throw new Error('No trusted proxy origins configured');
  }

  if (!trustedOrigins.includes(senderOrigin)) {
    throw new Error('Proxy origin is not trusted');
  }
}

function getUrlOrigin(url) {
  try {
    return new URL(url).origin;
  } catch {
    return null;
  }
}

// Restore on startup
chrome.storage.local.get(['proxyConfig'], (result) => {
  if (result.proxyConfig) {
    const { scheme, host, port, bypassList } = result.proxyConfig;
    chrome.proxy.settings.set({
      value: {
        mode: 'fixed_servers',
        rules: {
          singleProxy: { scheme, host, port },
          bypassList: bypassList || ['localhost', '127.0.0.1']
        }
      },
      scope: 'regular'
    });
  }
});

async function handleSetProxy(config) {
  if (!config || !config.host) {
    throw new Error('Proxy host is required');
  }

  const { host, port = 8080, scheme = 'http', bypassList = ['localhost', '127.0.0.1'] } = config;

  await chrome.proxy.settings.set({
    value: {
      mode: 'fixed_servers',
      rules: {
        singleProxy: { scheme, host, port },
        bypassList
      }
    },
    scope: 'regular'
  });

  const proxyConfig = { host, port, scheme, bypassList };
  await chrome.storage.local.set({ proxyConfig });

  return { configured: true, host, port, scheme };
}

async function handleClearProxy() {
  await chrome.proxy.settings.set({
    value: { mode: 'direct' },
    scope: 'regular'
  });
  await chrome.storage.local.remove(['proxyConfig']);
  return { cleared: true };
}
