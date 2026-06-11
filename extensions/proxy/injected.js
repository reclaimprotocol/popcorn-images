// Popcorn Proxy Extension - Injected Script
// Exposes stealth API for page-level proxy configuration

(function() {
  'use strict';

  // ── Kiosk geometry leak fix ────────────────────────────────────────────
  // Under `--kiosk` on Xdummy/Xvfb the window manager reports
  // window.outerWidth/outerHeight smaller than (or equal to, or zero against)
  // innerWidth/innerHeight. On real Chrome the outer dimensions are ALWAYS
  // >= the inner ones (the outer box includes the OS title bar + tab strip +
  // omnibox), so outer < inner is physically impossible and a deterministic
  // Akamai/Cloudflare signal. We clamp the outer dimensions to at least the
  // inner ones whenever the native value is smaller.
  //
  // Tradeoff: defining getters here leaves a JS toString trace on
  // Object.getOwnPropertyDescriptor(window,'outerHeight').get — a real fix
  // belongs in the CloakBrowser binary, but we don't control it, and an
  // impossible geometry is a far stronger tell than a getter trace. We only
  // override when the native geometry is actually broken so unaffected
  // environments keep the native descriptor.
  try {
    var fixDim = function (outerProp, innerProp) {
      var native = window[outerProp];
      var inner = window[innerProp];
      if (typeof native === 'number' && typeof inner === 'number'
          && native >= inner && native > 0) {
        return; // geometry already plausible — leave the native getter intact
      }
      Object.defineProperty(window, outerProp, {
        get: function () {
          // innerWidth/innerHeight read live so resizes stay consistent.
          return Math.max(window[innerProp], native || 0);
        },
        configurable: true,
        enumerable: true
      });
    };
    fixDim('outerWidth', 'innerWidth');
    fixDim('outerHeight', 'innerHeight');
  } catch (e) { /* never let the geometry fix break page load */ }

  const pendingRequests = new Map();
  let requestCounter = 0;

  window.addEventListener('message', (event) => {
    if (event.source !== window) return;

    const message = event.data;
    if (!message || message.type !== 'PCN_PROXY_RESPONSE') return;
    if (message.direction !== 'to-page') return;

    const pending = pendingRequests.get(message.requestId);
    if (pending) {
      pendingRequests.delete(message.requestId);
      if (message.success) {
        pending.resolve(message.result);
      } else {
        pending.reject(new Error(message.error || 'Unknown error'));
      }
    }
  });

  function sendToExtension(type, config) {
    return new Promise((resolve, reject) => {
      const requestId = ++requestCounter;
      pendingRequests.set(requestId, { resolve, reject });

      setTimeout(() => {
        if (pendingRequests.has(requestId)) {
          pendingRequests.delete(requestId);
          reject(new Error('Request timeout'));
        }
      }, 10000);

      window.postMessage({
        type: type,
        direction: 'to-extension',
        requestId: requestId,
        config: config
      }, '*');
    });
  }

  // Use non-obvious property name to avoid detection
  // Looks like an internal performance/config variable
  Object.defineProperty(window, '__pcn', {
    value: Object.freeze({
      set: function(config) {
        return sendToExtension('PCN_PROXY_SET', config);
      },
      clear: function() {
        return sendToExtension('PCN_PROXY_CLEAR', null);
      },
      get: function() {
        return sendToExtension('PCN_PROXY_GET', null);
      },
      ready: true
    }),
    writable: false,
    configurable: false,
    enumerable: false
  });

  window.dispatchEvent(new CustomEvent('__pcnReady'));
})();
