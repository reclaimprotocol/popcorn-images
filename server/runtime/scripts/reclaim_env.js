window.ReclaimMessenger = {
  _send: (event, message) => {
    // Use console.log with __RECLAIM__ prefix to communicate with backend
    // This avoids using exposeBinding which triggers Cloudflare detection
    try {
      console.log('__RECLAIM__' + JSON.stringify({ event, message }));
      return true;
    } catch (e) {
      console.error('Failed to send Reclaim message:', e);
      return false;
    }
  },
  log: (logType, message) => {
    switch (logType) {
      case "error":
        if (window.ReclaimMessenger._send("log", { level: 'error', message })) return;
        console.error(message);
        break;
      default:
        if (window.ReclaimMessenger._send("log", { level: 'info', message })) return;
        console.log(message);
        break;
    }
  },
  send: (event, message) => {
    if (window.ReclaimMessenger._send(event, message)) {
      return;
    }

    window.ReclaimMessenger.log("error", {
      reason: `failed to send message, unknown environment`,
      event,
      message,
    });
  },
};

window.Reclaim = {
  version: 1,
  provider: {},
  parameters: {},
  requestClaim: (claim) => {
    const message = claim;
    if (!('url' in message) || !('method' in message) || !('responseMatches' in message) || !Array.isArray(message.responseMatches) || (typeof message.url !== 'string') || (typeof message.method !== 'string')) {
      window.Reclaim.log(
        'error',
        `required fields 'url', 'method' or 'responseMatches' missing in arguments for Reclaim.requestClaim(message:)`
      );
    }
    window.ReclaimMessenger.send("requestClaim", claim);
  },
  requiresUserInteraction: (isUserInteractionRequired) => {
    window.ReclaimMessenger.send("requiresUserInteraction", {
      value: !!isUserInteractionRequired,
    });
  },
  canExpectManyClaims: (canExpectManyClaims) => {
    window.ReclaimMessenger.send("canExpectManyClaims", {
      value: !!canExpectManyClaims,
    });
  },
  updatePublicData: (data) => {
    window.ReclaimMessenger.send("publicData", data);
  },
  reportProviderError: (error) => {
    if (typeof error === "string") {
      error = { message: error };
    }
    window.ReclaimMessenger.send("reportProviderError", error);
  },
  // log(logType: 'error' | 'info', message: object)
  log: (logType, message) => {
    window.ReclaimMessenger.log(logType, message);
  },
};

const sendClaimResponseOnDocumentContentLoaded = (
  url,
  requestMethod,
  contentType,
  responseText
) => {
  const headers = {
    Accept:
      "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
    // Following Sec-* headers can't be set from fetch.
    "Sec-Fetch-Site": "none",
    "Sec-Fetch-Mode": "navigate",
    "Sec-Fetch-User": "?1",
    "Sec-Fetch-Dest": "document",
  };
  const requestBody = "";

  window.ReclaimMessenger.send("request", {
    requestBody:
      typeof requestBody === "string"
        ? requestBody
        : requestBody !== null && typeof requestBody === "object"
          ? JSON.stringify(requestBody)
          : "",
    url: url || "",
    headers: headers,
    responseBody: responseText || "",
    method: requestMethod || "",
    currentPageUrl: window.location.href || "",
    contentType: headers["content-type"] || headers["Content-Type"] || "",
    metadata: {
      loadEventStart: new Date(
        window.performance.timeOrigin +
        window.performance.getEntriesByType("navigation")[0].loadEventStart
      ).toISOString(),
      receivedAt: new Date().toISOString(),
    },
  });
};

window.setupHawkeye = (options) => {
  try {
    window.Reclaim.log(
      'info',
      "Setting up Hawkeye with window.reclaimInterceptor"
    );

    // Create instance with debug options
    const interceptor = new window.HawkeyeRequestInterceptor({
      disableFetch: options.disableFetch == true, // Set to true to disable fetch interception
      disableXHR: options.disableXHR == true, // Set to true to disable XHR interception
      disableFormIntercept: options.disableFormIntercept == true, // Set to true to disable form submission interception
      delayFormSubmitForFetch: options.delayFormSubmitForFetch == true, // Set to true to wait for fetch before form navigation
      useProxyForFetch: options.useProxyForFetch == true, // Set to false to use direct replacement instead of Proxy (default: true)
      useGetterForFetch: options.useGetterForFetch == true, // Set to true to use getter/setter approach (most robust)
    });

    // Example middleware for logging requests with ID
    interceptor.addRequestMiddleware(async (requestData) => {
      window.Reclaim.log('info', `[${requestData.id}] Request: ${JSON.stringify({
        url: requestData.url,
        method: requestData.method,
        headers: requestData.headers,
      })}`);
    }, "request_logger");

    // Example middleware for logging responses with ID
    interceptor.addResponseMiddleware(async (response, requestData) => {
      window.Reclaim.log('info', `[${requestData.id}] Response: ${JSON.stringify({
        url: requestData.url,
        status: response.status,
        body:
          typeof response.body === "string"
            ? response.body.substring(0, 100) + "..."
            : response.body,
      })}`);
    }, "response_logger");

    window.reclaimInterceptor = interceptor;

    window.Reclaim.log(
      'info',
      "Userscript initialized and ready - Access via window.reclaimInterceptor"
    );

    window.reclaimInterceptor.addResponseMiddleware(async (response, request) => {
      try {
        window.Reclaim.log(
          "info",
          'intercepted',
        );
        let requestUrl = request.url;
        if (typeof requestUrl !== "string") {
          window.Reclaim.log(
            'info', {
            reason: "request.url is not a string",
            url: requestUrl,
            request: request,
            type: typeof requestUrl,
          });
          if ("href" in requestUrl) {
            requestUrl = requestUrl.href;
          } else if (typeof requestUrl === "object" && "url" in requestUrl) {
            requestUrl = requestUrl.url;
          } else {
            requestUrl = requestUrl || "/";
          }
        }

        const url = requestUrl.startsWith("/")
          ? window.location.origin + requestUrl
          : requestUrl;
        let requestMethod =
          request.method ||
          (request.options.method ? request.options.method : "GET");
        let parsedHeaders = {};
        const receivedHeaders = request.headers || request.options.headers;
        if (receivedHeaders && receivedHeaders.get) {
          parsedHeaders = Object.fromEntries(receivedHeaders);
        } else {
          parsedHeaders = receivedHeaders;
        }
        let responseText;
        responseText = response.body;
        if (typeof responseText !== "string") {
          try {
            responseText = JSON.stringify(responseText);
          } catch (e) {
            window.Reclaim.log(
              'error', "Failed to stringify responseText", e);
            responseText = responseText.toString();
          }
        }
        const headers = parsedHeaders;
        let requestBody =
          request.body || (request.options ? request.options.body : null);

        window.Reclaim.log(
          "info",
          'intercepted: ' + url,
        );

        window.ReclaimMessenger.send("request", {
          requestBody:
            typeof requestBody === "string"
              ? requestBody
              : requestBody !== null && typeof requestBody === "object"
                ? JSON.stringify(requestBody)
                : "",
          url: url || "",
          headers: headers,
          responseBody: responseText || "",
          method: requestMethod || "",
          currentPageUrl: window.location.href || "",
          contentType: headers["content-type"] || headers["Content-Type"] || "",
          metadata: {
            loadEventStart: new Date(
              window.performance.timeOrigin +
              window.performance.getEntriesByType("navigation")[0]
                .loadEventStart
            ).toISOString(),
            receivedAt: new Date().toISOString(),
          },
        });
      } catch (error) {
        window.Reclaim.log(
          "error",
          error.message,
        );
      }
    }, 'reclaim_api_interceptor');

    window.Reclaim.log('info', "Hawkeye interceptor initialized");
  } catch (error) {
    window.Reclaim.log('error', `Failed to initialize Hawkeye interceptor: ${error}`);
  }
};

const _handleRequestReplay = () => {
  const isHawkeyeInjection = window.Reclaim.provider.injectionType?.toUpperCase() === 'HAWKEYE';
  const canReplayDocumentRequest = window.Reclaim.provider.disableRequestReplay != true && isHawkeyeInjection;

  window.Reclaim.log(
    "info",
    `_onContentLoaded(canReplayDocumentRequest:${canReplayDocumentRequest}, injectionType:${window.Reclaim.provider.injectionType})`,
  );

  const identifiedDocumentMethod = 'GET';
  if (canReplayDocumentRequest) {
    fetch(window.location.href, {
      method: identifiedDocumentMethod,
      headers: {
        Accept:
          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
      },
    }).then(async (_) => { });
    return;
  }

  const preNode = document.evaluate(
    "//pre",
    document,
    null,
    XPathResult.FIRST_ORDERED_NODE_TYPE,
    null
  ).singleNodeValue;
  const preText = preNode ? preNode.innerText : null;
  if (preText) {
    window._ResponseOnDocumentContentLoaded = preText;
  } else {
    window._ResponseOnDocumentContentLoaded =
      document.documentElement.innerHTML;
  }
  if (typeof window._On_ResponseOnDocumentContentLoaded === "function") {
    sendClaimResponseOnDocumentContentLoaded(
      location.href,
      identifiedDocumentMethod,
      document.contentType,
      window._ResponseOnDocumentContentLoaded
    );
  }
}

/**
 * {'url' | 'element' | 'timeout' | 'none'}
 */
let maybeRequiresLoginInteraction = 'none';
const lastNotifiedIndicator = {
  indicator: null,
  page: null,
};

/**
 * 
 * @param {'url' | 'element' | 'timeout' | 'none'} value 
 */
const notifyLoginInteraction = (value) => {
  if (window.location.href === 'about:blank') {
    return;
  }
  const isInIframe = window !== window.parent;
  if (isInIframe) {
    return;
  }
  const newIndicatorValue = value;
  if (lastNotifiedIndicator.indicator == newIndicatorValue && lastNotifiedIndicator.page == window.location.href) {
    // don't notify if same as previous update
    return;
  }
  lastNotifiedIndicator.indicator = newIndicatorValue;
  lastNotifiedIndicator.page = window.location.href;
  window.ReclaimMessenger.send('maybeRequiresLoginInteraction', { value: value !== 'none', currentPageUrl: window.location.href, hasFocus: document.hasFocus(), indicator: value });
}

(function initGlobalFocusLogger() {
  // A helper function to check if the focused element is actually an input type
  function isInputElement(element) {
    if (!element || !element.tagName) return false;
    const tag = element.tagName.toUpperCase();
    // You can add 'SELECT' or 'BUTTON' here if you want to track those as well
    return tag === 'INPUT' || tag === 'TEXTAREA';
  }

  // Triggered whenever an element gains focus
  document.addEventListener('focusin', function (event) {
    const target = event.target;

    if (isInputElement(target)) {
      // Your custom logging logic goes here
      window.Reclaim.log('info', '[Reclaim.log] Input Focused: ' + JSON.stringify({
        type: target.type,
        name: target.name || 'unnamed',
        id: target.id || 'no-id',
        //value: target.value
      }));
    }
  });

  // Triggered whenever an element loses focus
  document.addEventListener('focusout', function (event) {
    const target = event.target;

    if (isInputElement(target)) {
      // Your custom logging logic goes here
      window.Reclaim.log('info', '[Reclaim.log] Input Blurred: ' + JSON.stringify({
        type: target.type,
        name: target.name || 'unnamed',
        id: target.id || 'no-id',
        //value: target.value
      }));
    }
  });
})();

const _onContentLoaded = () => {
  _handleRequestReplay();
  maybeRequiresLoginInteraction = window.__maybeRequiresLoginInteraction();
  notifyLoginInteraction(maybeRequiresLoginInteraction);
  const intervalId = setInterval(() => {
    const now = window.__maybeRequiresLoginInteraction();
    maybeRequiresLoginInteraction = now;
    notifyLoginInteraction(maybeRequiresLoginInteraction);
  }, 2500);

  let timeoutInterval = 0;

  timeoutInterval = setInterval(() => {
    if (maybeRequiresLoginInteraction !== 'url') {
      clearInterval(intervalId);
      clearInterval(timeoutInterval);
      maybeRequiresLoginInteraction = 'timeout';
      notifyLoginInteraction(maybeRequiresLoginInteraction);
    }
  }, 30 * 1000);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', _onContentLoaded, { once: true });
} else {
  _onContentLoaded();
}
