// Popcorn Proxy Extension - Content Script
// Bridges between page context and extension background

(function() {
  'use strict';

  // Inject the page-level script
  const script = document.createElement('script');
  script.src = chrome.runtime.getURL('injected.js');
  script.onload = function() {
    this.remove();
  };
  (document.head || document.documentElement).appendChild(script);

  // Listen for messages from the injected script
  window.addEventListener('message', async (event) => {
    if (event.source !== window) return;
    if (typeof event.origin !== 'string' || event.origin === 'null' || event.origin === 'undefined') return;

    const message = event.data;
    if (!message || !message.type || !message.type.startsWith('PCN_PROXY_')) return;
    if (message.direction !== 'to-extension') return;
    if (event.origin !== window.location.origin) return;

    const requestId = message.requestId;

    try {
      const response = await chrome.runtime.sendMessage({
        type: message.type,
        config: message.config,
        requestOrigin: event.origin
      });

      window.postMessage({
        type: 'PCN_PROXY_RESPONSE',
        direction: 'to-page',
        requestId: requestId,
        success: response.success,
        result: response.result || response.config,
        error: response.error
      }, event.origin);
    } catch (error) {
      window.postMessage({
        type: 'PCN_PROXY_RESPONSE',
        direction: 'to-page',
        requestId: requestId,
        success: false,
        error: error.message
      }, event.origin);
    }
  });
})();
