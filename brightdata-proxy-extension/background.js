// Bright Data Proxy Extension
//
// @note: for the polling
// setInterval timers gets cancelled wen service workers get terminated :(
// chrome.alarms persists across service worker restarts and seems to be working
//
// References:
// - https://developer.chrome.com/docs/extensions/develop/migrate/to-service-workers#alarms
// - https://developer.chrome.com/docs/extensions/develop/concepts/service-workers/lifecycle
// - https://issues.chromium.org/issues/40733525
//
// Interval is 30 seconds to avoid frequent proxy reconnects which can cause IP rotation.
const CONFIG_URL = "http://127.0.0.1:10001/proxy/config";
const ALARM_NAME = "proxyConfigPoll";
const POLL_INTERVAL_MINUTES = 0.5;

// Keep credentials in memory for fast sync access
let cachedCredentials = null;

// Fetch proxy configuration from API
async function fetchProxyConfig() {
  try {
    const response = await fetch(CONFIG_URL);
    if (!response.ok) {
      console.error("Failed to fetch proxy config:", response.status);
      return null;
    }
    return await response.json();
  } catch (error) {
    console.error("Error fetching proxy config:", error);
    return null;
  }
}

// Simple hash function to detect config changes
function hashConfig(config) {
  if (!config || !config.host) return null;
  return JSON.stringify({
    host: config.host,
    port: config.port,
    username: config.username,
    password: config.password,
    scheme: config.scheme,
    bypassList: config.bypassList
  });
}

// Apply proxy settings
async function applyProxySettings(config) {
  if (!config || !config.host || !config.port) {
    chrome.proxy.settings.clear({ scope: "regular" });
    await chrome.storage.local.set({ proxyConfig: null, lastConfigHash: null });
    cachedCredentials = null;
    return;
  }

  await chrome.storage.local.set({ proxyConfig: config, lastConfigHash: hashConfig(config) });
  cachedCredentials = config;

  chrome.proxy.settings.set({
    value: {
      mode: "fixed_servers",
      rules: {
        singleProxy: {
          scheme: config.scheme || "http",
          host: config.host,
          port: parseInt(config.port, 10)
        },
        bypassList: config.bypassList || ["localhost", "127.0.0.1"]
      }
    },
    scope: "regular"
  });
}

// Check for config changes and apply if needed
async function checkAndApplyConfig() {
  const config = await fetchProxyConfig();
  const newHash = hashConfig(config);
  const stored = await chrome.storage.local.get(["lastConfigHash"]);

  if (newHash !== stored.lastConfigHash) {
    await applyProxySettings(config);
  }
}

// Update cached credentials whenever config changes
async function updateCachedCredentials() {
  const stored = await chrome.storage.local.get(["proxyConfig"]);
  cachedCredentials = stored.proxyConfig;
}

// Handle proxy authentication
chrome.webRequest.onAuthRequired.addListener(
  (_details, callbackFn) => {
    if (cachedCredentials && cachedCredentials.username && cachedCredentials.password) {
      callbackFn({
        authCredentials: {
          username: cachedCredentials.username,
          password: cachedCredentials.password
        }
      });
    } else {
      chrome.storage.local.get(["proxyConfig"]).then(stored => {
        if (stored.proxyConfig && stored.proxyConfig.username && stored.proxyConfig.password) {
          cachedCredentials = stored.proxyConfig;
          callbackFn({
            authCredentials: {
              username: stored.proxyConfig.username,
              password: stored.proxyConfig.password
            }
          });
        } else {
          callbackFn({ cancel: false });
        }
      });
    }
  },
  { urls: ["<all_urls>"] },
  ["asyncBlocking"]
);

// Handle alarm for periodic config check
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM_NAME) {
    checkAndApplyConfig();
  }
});

// Initialize on service worker start
async function init() {
  await updateCachedCredentials();
  await checkAndApplyConfig();
  await chrome.alarms.create(ALARM_NAME, { periodInMinutes: POLL_INTERVAL_MINUTES });
}

// Run on install/update
chrome.runtime.onInstalled.addListener(() => {
  init();
});

// Also run on service worker startup
init();
