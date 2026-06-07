// Reclaim runtime, single-namespace edition.
//
// All state and helpers live inside this IIFE's closure; the only thing
// touching `window` is `window.Reclaim` itself, installed non-enumerable.
// Anti-bot scanners that walk `Object.getOwnPropertyNames(window)` see at
// most one Reclaim-named entry instead of half a dozen.
//
// Provider customInjection scripts call `Reclaim.requestClaim(...)` etc.
// by name — that name is irreducible. Everything else (drain, hawkeye
// wiring, login-check registration, SAML auto-submit dedup) hangs off
// `Reclaim._` private methods so the runtime has internal entry points
// without leaking additional global names.
(function () {
  'use strict';

  // ── Closure state ────────────────────────────────────────────────────
  let outbox = [];
  let HawkeyeClass = null;     // populated by hawkeye.js via Reclaim._registerHawkeyeClass
  let hawkeyeReady = false;     // true after setupHawkeyeImpl successfully wires up
  let loginCheckFn = null;      // populated by login_script.js via Reclaim._setLoginCheck
  let samlSubmitted = false;    // SAML auto-submit dedup (per-document)

  // ── Internal messenger (drains via Reclaim._drain) ───────────────────
  const messenger = {
    _send: function (event, message) {
      try {
        outbox.push({ event, message });
        return true;
      } catch (e) {
        try { console.error('Failed to send Reclaim message:', e); } catch (_) { }
        return false;
      }
    },
    log: function (logType, message) {
      switch (logType) {
        case 'error':
          if (messenger._send('log', { level: 'error', message })) return;
          console.error(message);
          break;
        default:
          if (messenger._send('log', { level: 'info', message })) return;
          console.log(message);
          break;
      }
    },
    send: function (event, message) {
      if (messenger._send(event, message)) return;
      messenger.log('error', {
        reason: 'failed to send message, unknown environment',
        event,
        message,
      });
    },
  };

  // ── Hawkeye setup (uses HawkeyeClass from closure) ───────────────────
  function setupHawkeyeImpl(options) {
    try {
      if (!HawkeyeClass) {
        messenger.log('error', 'Hawkeye class not registered; setupHawkeye called before hawkeye.js loaded');
        return;
      }
      messenger.log('info', 'Setting up Hawkeye');
      const inst = new HawkeyeClass({
        disableFetch: options.disableFetch == true,
        disableXHR: options.disableXHR == true,
        disableFormIntercept: options.disableFormIntercept == true,
        delayFormSubmitForFetch: options.delayFormSubmitForFetch == true,
        useProxyForFetch: options.useProxyForFetch == true,
        useGetterForFetch: options.useGetterForFetch == true,
      });

      inst.addRequestMiddleware(async (requestData) => {
        messenger.log('info', `[${requestData.id}] Request: ${JSON.stringify({
          url: requestData.url,
          method: requestData.method,
          headers: requestData.headers,
        })}`);
      }, 'request_logger');

      inst.addResponseMiddleware(async (response, requestData) => {
        messenger.log('info', `[${requestData.id}] Response: ${JSON.stringify({
          url: requestData.url,
          status: response.status,
          body: typeof response.body === 'string'
            ? response.body.substring(0, 100) + '...'
            : response.body,
        })}`);
      }, 'response_logger');

      hawkeyeReady = true;
      messenger.log('info', 'Hawkeye initialized');

      inst.addResponseMiddleware(async (response, request) => {
        try {
          messenger.log('info', 'intercepted');
          let requestUrl = request.url;
          if (typeof requestUrl !== 'string') {
            messenger.log('info', {
              reason: 'request.url is not a string',
              url: requestUrl,
              request: request,
              type: typeof requestUrl,
            });
            if ('href' in requestUrl) {
              requestUrl = requestUrl.href;
            } else if (typeof requestUrl === 'object' && 'url' in requestUrl) {
              requestUrl = requestUrl.url;
            } else {
              requestUrl = requestUrl || '/';
            }
          }
          const url = requestUrl.startsWith('/')
            ? window.location.origin + requestUrl
            : requestUrl;
          let requestMethod = request.method || (request.options.method ? request.options.method : 'GET');
          let parsedHeaders = {};
          const receivedHeaders = request.headers || request.options.headers;
          if (receivedHeaders && receivedHeaders.get) {
            parsedHeaders = Object.fromEntries(receivedHeaders);
          } else {
            parsedHeaders = receivedHeaders;
          }
          let responseText = response.body;
          if (typeof responseText !== 'string') {
            try {
              responseText = JSON.stringify(responseText);
            } catch (e) {
              messenger.log('error', 'Failed to stringify responseText', e);
              responseText = responseText.toString();
            }
          }
          const headers = parsedHeaders;
          const requestBody = request.body || (request.options ? request.options.body : null);

          messenger.log('info', 'intercepted: ' + url);

          messenger.send('request', {
            requestBody:
              typeof requestBody === 'string'
                ? requestBody
                : requestBody !== null && typeof requestBody === 'object'
                  ? JSON.stringify(requestBody)
                  : '',
            url: url || '',
            headers,
            responseBody: responseText || '',
            method: requestMethod || '',
            currentPageUrl: window.location.href || '',
            contentType: headers['content-type'] || headers['Content-Type'] || '',
            metadata: {
              loadEventStart: new Date(
                window.performance.timeOrigin +
                window.performance.getEntriesByType('navigation')[0].loadEventStart
              ).toISOString(),
              receivedAt: new Date().toISOString(),
            },
          });
        } catch (error) {
          messenger.log('error', error.message);
        }
      }, 'reclaim_api_interceptor');
    } catch (error) {
      messenger.log('error', `Failed to initialize Hawkeye interceptor: ${error}`);
    }
  }

  // ── Document-load handlers (request replay + login-check polling) ────
  function sendClaimResponseOnDocumentContentLoaded(url, requestMethod, _contentType, responseText) {
    const headers = {
      Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7',
      'Sec-Fetch-Site': 'none',
      'Sec-Fetch-Mode': 'navigate',
      'Sec-Fetch-User': '?1',
      'Sec-Fetch-Dest': 'document',
    };
    const requestBody = '';
    messenger.send('request', {
      requestBody:
        typeof requestBody === 'string'
          ? requestBody
          : requestBody !== null && typeof requestBody === 'object'
            ? JSON.stringify(requestBody)
            : '',
      url: url || '',
      headers,
      responseBody: responseText || '',
      method: requestMethod || '',
      currentPageUrl: window.location.href || '',
      contentType: headers['content-type'] || headers['Content-Type'] || '',
      metadata: {
        loadEventStart: new Date(
          window.performance.timeOrigin +
          window.performance.getEntriesByType('navigation')[0].loadEventStart
        ).toISOString(),
        receivedAt: new Date().toISOString(),
      },
    });
  }

  function handleRequestReplay() {
    const isHawkeyeInjection = __ReclaimAPI.provider.injectionType?.toUpperCase() === 'HAWKEYE';
    const canReplayDocumentRequest = __ReclaimAPI.provider.disableRequestReplay != true && isHawkeyeInjection;

    messenger.log('info', `_onContentLoaded(canReplayDocumentRequest:${canReplayDocumentRequest}, injectionType:${__ReclaimAPI.provider.injectionType})`);

    const identifiedDocumentMethod = 'GET';
    if (canReplayDocumentRequest) {
      fetch(window.location.href, {
        method: identifiedDocumentMethod,
        headers: {
          Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7',
        },
      }).then(async (_) => { });
      return;
    }

    const preNode = document.evaluate(
      '//pre',
      document,
      null,
      XPathResult.FIRST_ORDERED_NODE_TYPE,
      null
    ).singleNodeValue;
    const preText = preNode ? preNode.innerText : null;
    const responseText = preText || document.documentElement.innerHTML;
    sendClaimResponseOnDocumentContentLoaded(
      location.href,
      identifiedDocumentMethod,
      document.contentType,
      responseText
    );
  }

  let maybeRequiresLoginInteraction = 'none';
  const lastNotifiedIndicator = { indicator: null, page: null };

  function notifyLoginInteraction(value) {
    if (window.location.href === 'about:blank') return;
    if (window !== window.parent) return;  // skip iframes
    if (lastNotifiedIndicator.indicator == value && lastNotifiedIndicator.page == window.location.href) return;
    lastNotifiedIndicator.indicator = value;
    lastNotifiedIndicator.page = window.location.href;
    messenger.send('maybeRequiresLoginInteraction', {
      value: value !== 'none',
      currentPageUrl: window.location.href,
      hasFocus: document.hasFocus(),
      indicator: value,
    });
  }

  function onContentLoaded() {
    handleRequestReplay();
    const runCheck = () => (loginCheckFn ? loginCheckFn() : 'none');
    maybeRequiresLoginInteraction = runCheck();
    notifyLoginInteraction(maybeRequiresLoginInteraction);
    const intervalId = setInterval(() => {
      const now = runCheck();
      maybeRequiresLoginInteraction = now;
      notifyLoginInteraction(maybeRequiresLoginInteraction);
    }, 2500);
    let timeoutInterval = setInterval(() => {
      if (maybeRequiresLoginInteraction !== 'url') {
        clearInterval(intervalId);
        clearInterval(timeoutInterval);
        maybeRequiresLoginInteraction = 'timeout';
        notifyLoginInteraction(maybeRequiresLoginInteraction);
      }
    }, 30 * 1000);
  }

  // ── SAML interstitial auto-submit ────────────────────────────────────
  //
  // Many IdPs (Okta, Azure AD, Auth0, Ping, generic SAML SPs) return a
  // "Signing in..." HTML shell containing a hidden form with SAMLResponse
  // + RelayState that's *meant* to auto-submit via inline JS. Under
  // popcorn's locked-down context, that inline JS sometimes doesn't fire
  // and the page hangs blank. We watch the DOM for the form and submit it
  // ourselves the moment it appears — no polling, no URL heuristics, fires
  // for any IdP that uses the standard SAMLResponse-form pattern.
  // Strict shape check — only submit forms that are unambiguous SAML POST
  // interstitials, never pages that merely contain a SAMLResponse field
  // (IdP admin consoles, SAML debugger tools, half-rendered UIs, etc.).
  // Conservative on purpose: better to miss an edge-case interstitial than
  // to submit on a page the user is supposed to interact with.
  function isAutoSubmitSamlForm(form, input) {
    if (!form || !input) return false;
    // 1. Form must be a POST with a usable action.
    const method = (form.method || '').toUpperCase();
    if (method !== 'POST') return false;
    if (!form.action || typeof form.action !== 'string') return false;
    // 2. The SAMLResponse input must be hidden and carry a real-looking
    //    base64 payload. Real responses are typically >= a few hundred
    //    bytes; we use 100 as a loose floor.
    if (input.type && input.type.toLowerCase() !== 'hidden') return false;
    const value = input.value || '';
    if (value.length < 100) return false;
    // 3. The form must have NO user-facing inputs. An auto-submit
    //    interstitial has only hidden fields (SAMLResponse, RelayState).
    //    If there's a visible text/password/email/etc. input, it's a
    //    different page the user is supposed to interact with.
    const allInputs = form.querySelectorAll('input, select, textarea');
    for (let i = 0; i < allInputs.length; i++) {
      const el = allInputs[i];
      const tag = el.tagName.toUpperCase();
      if (tag === 'SELECT' || tag === 'TEXTAREA') return false;
      const t = (el.type || '').toLowerCase();
      if (t && t !== 'hidden' && t !== 'submit' && t !== 'button') return false;
    }
    // 4. Sanity check: SAMLResponse value is plausibly base64. Cheap regex
    //    catches placeholders, lorem-ipsum text, or "EXAMPLE_VALUE" stubs.
    if (!/^[A-Za-z0-9+/=\s]+$/.test(value)) return false;
    return true;
  }

  function trySubmitSamlForm() {
    if (samlSubmitted) return false;
    const inputs = document.querySelectorAll('input[name="SAMLResponse"]');
    for (let i = 0; i < inputs.length; i++) {
      const input = inputs[i];
      const form = input.form;
      if (!isAutoSubmitSamlForm(form, input)) continue;
      samlSubmitted = true;
      try { form.submit(); } catch (_) { /* ignore */ }
      try {
        messenger.send('samlAutoSubmitted', {
          action: form.action,
          currentPageUrl: window.location.href,
          at: new Date().toISOString(),
        });
      } catch (_) { /* ignore */ }
      return true;
    }
    return false;
  }
  function installSamlObserver() {
    // Immediate scan first — covers the case where the form is already
    // present at the time this code runs (e.g. document.readyState !==
    // 'loading' on script execution).
    if (trySubmitSamlForm()) return;

    // MutationObserver fires on every subtree change. Cheap, only inspects
    // newly-added nodes; bails as soon as a SAML form is found and submitted.
    const obs = new MutationObserver(function (mutations) {
      if (samlSubmitted) { obs.disconnect(); return; }
      for (let m = 0; m < mutations.length; m++) {
        const added = mutations[m].addedNodes;
        for (let a = 0; a < added.length; a++) {
          const node = added[a];
          if (!node || node.nodeType !== 1) continue;
          // Either the added node itself is a SAMLResponse input, or its
          // subtree contains one. querySelector on the whole document is
          // fine here — it's only run when something actually changed, and
          // bails on first match.
          if (
            (node.tagName === 'INPUT' && node.getAttribute && node.getAttribute('name') === 'SAMLResponse') ||
            (typeof node.querySelector === 'function' && node.querySelector('input[name="SAMLResponse"]'))
          ) {
            if (trySubmitSamlForm()) { obs.disconnect(); return; }
          }
        }
      }
    });
    try {
      obs.observe(document.documentElement || document, { childList: true, subtree: true });
    } catch (_) { /* document.documentElement not ready yet */ }

    // Safety stop: if the page sits for >30s without ever rendering a SAML
    // form, disconnect the observer to free resources. Real SAML
    // interstitials resolve within seconds; anything longer is a different
    // page that doesn't need our auto-submit.
    setTimeout(function () {
      try { if (!samlSubmitted) obs.disconnect(); } catch (_) {}
    }, 30000);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', installSamlObserver, { once: true });
  } else {
    installSamlObserver();
  }

  // ── Input focus logging (closure-only event listeners) ───────────────
  (function initGlobalFocusLogger() {
    function isInputElement(element) {
      if (!element || !element.tagName) return false;
      const tag = element.tagName.toUpperCase();
      return tag === 'INPUT' || tag === 'TEXTAREA';
    }
    document.addEventListener('focusin', function (event) {
      const target = event.target;
      if (isInputElement(target)) {
        messenger.log('info', '[Reclaim.log] Input Focused: ' + JSON.stringify({
          type: target.type,
          name: target.name || 'unnamed',
          id: target.id || 'no-id',
        }));
      }
    });
    document.addEventListener('focusout', function (event) {
      const target = event.target;
      if (isInputElement(target)) {
        messenger.log('info', '[Reclaim.log] Input Blurred: ' + JSON.stringify({
          type: target.type,
          name: target.name || 'unnamed',
          id: target.id || 'no-id',
        }));
      }
    });
  })();

  // ── Public API (single window export) ─────────────────────────────────
  const __ReclaimAPI = {
    version: 1,
    provider: {},
    parameters: {},
    requestClaim: function (claim) {
      const message = claim;
      if (
        !('url' in message) ||
        !('method' in message) ||
        !('responseMatches' in message) ||
        !Array.isArray(message.responseMatches) ||
        typeof message.url !== 'string' ||
        typeof message.method !== 'string'
      ) {
        messenger.log('error', "required fields 'url', 'method' or 'responseMatches' missing in arguments for Reclaim.requestClaim(message:)");
      }
      messenger.send('requestClaim', claim);
    },
    requiresUserInteraction: function (v) { messenger.send('requiresUserInteraction', { value: !!v }); },
    canExpectManyClaims: function (v) { messenger.send('canExpectManyClaims', { value: !!v }); },
    updatePublicData: function (data) { messenger.send('publicData', data); },
    reportProviderError: function (error) {
      if (typeof error === 'string') error = { message: error };
      messenger.send('reportProviderError', error);
    },
    reportUserLoggedIn: function () { messenger.send('reportUserLoggedIn', {}); },
    log: function (logType, message) { messenger.log(logType, message); },

    // ── Internal entry points (still public-callable, prefixed with _) ─
    // Backend drains the outbox via Reclaim._drain(). Closure-private state.
    _drain: function () { const x = outbox; outbox = []; return x; },
    // Generic event send for non-API channels (capmonster.js, interceptor_notifier.js).
    // Replaces the legacy window.ReclaimMessenger.send export.
    _notify: function (event, message) { messenger.send(event, message); },
    // hawkeye.js registers the class with us instead of polluting window.
    _registerHawkeyeClass: function (cls) { HawkeyeClass = cls; },
    // login_script.js registers its login-check function with us.
    _setLoginCheck: function (fn) { loginCheckFn = fn; },
    // Hawkeye setup. Replaces the legacy window.setupHawkeye export.
    _setupHawkeye: function (options) { setupHawkeyeImpl(options); },
    // Introspection for the post-navigation safety net (web-page-manager).
    // Backend probes these via page.evaluate to decide whether to re-inject.
    _hasHawkeyeClass: function () { return !!HawkeyeClass; },
    _isHawkeyeReady: function () { return hawkeyeReady; },
    // SAML auto-submit dedup (web-page-manager safety net reads/writes this).
    _samlSubmitted: function () { return samlSubmitted; },
    _markSamlSubmitted: function () { samlSubmitted = true; },
  };

  try {
    Object.defineProperty(window, 'Reclaim', {
      value: __ReclaimAPI,
      enumerable: false,
      configurable: true,
      writable: true,
    });
  } catch (_) {
    window.Reclaim = __ReclaimAPI;
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', onContentLoaded, { once: true });
  } else {
    onContentLoaded();
  }
})();
