// Popcorn Proxy Extension - Content Script
// Bridges between page context and extension background

(function() {
  'use strict';

  // injected.js runs as a MAIN-world content script (see manifest.json), so
  // there's no script-tag injection here. Injecting via
  // <script src="chrome-extension://<id>/injected.js"> + a web_accessible_
  // resource is a stealth leak: an early MutationObserver sees the
  // chrome-extension:// URL, and any page can fetch the resource to confirm
  // the extension and its ID. The MAIN-world content-script path leaves no
  // such trace. This ISOLATED-world script is just the message bridge to the
  // background service worker (MAIN world has no chrome.runtime access).

  // Listen for messages from the injected script
  window.addEventListener('message', async (event) => {
    if (event.source !== window) return;

    const message = event.data;
    if (!message || !message.type || !message.type.startsWith('PCN_PROXY_')) return;
    if (message.direction !== 'to-extension') return;

    const requestId = message.requestId;

    try {
      const response = await chrome.runtime.sendMessage({
        type: message.type,
        config: message.config
      });

      window.postMessage({
        type: 'PCN_PROXY_RESPONSE',
        direction: 'to-page',
        requestId: requestId,
        success: response.success,
        result: response.result || response.config,
        error: response.error
      }, '*');
    } catch (error) {
      window.postMessage({
        type: 'PCN_PROXY_RESPONSE',
        direction: 'to-page',
        requestId: requestId,
        success: false,
        error: error.message
      }, '*');
    }
  });
})();
