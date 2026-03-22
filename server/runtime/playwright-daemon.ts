/**
 * Persistent Playwright Executor Daemon
 *
 * Listens on a Unix socket for code execution requests, maintains a warm CDP
 * connection to the browser, and uses esbuild for TypeScript transformation.
 *
 * Protocol (newline-delimited JSON):
 *
 * Execute (original):
 * Request:  { "id": string, "code": string, "timeout_ms"?: number }
 * Response: { "id": string, "success": boolean, "result"?: any, "error"?: string, "stack"?: string }
 *
 * Session commands (new):
 * Request:  { "id": string, "command": "init_session" | "get_captured_data" | "close_session", "params"?: object }
 * Response: { "id": string, "success": boolean, "result"?: any, "error"?: string }
 */

import { createServer, Socket } from 'net';
import { unlinkSync, existsSync, readFileSync, writeFileSync, mkdirSync } from 'fs';
import { join } from 'path';
import { transform } from 'esbuild';
import { chromium as chromiumPW, Browser, Page, BrowserContext } from 'playwright-core';
import { chromium as chromiumPR } from 'patchright';

const SOCKET_PATH = process.env.PLAYWRIGHT_DAEMON_SOCKET || '/tmp/playwright-daemon.sock';
const CDP_ENDPOINT = process.env.CDP_ENDPOINT || 'ws://127.0.0.1:9222';
const USE_PATCHRIGHT = process.env.PLAYWRIGHT_ENGINE !== 'playwright-core';
const RECONNECT_DELAY_MS = 1000;
const MAX_RECONNECT_ATTEMPTS = 10;
const SCRIPTS_DIR = process.env.BROWSER_EVENTS_SCRIPTS_DIR || '/usr/local/lib/browser-events-scripts';
const MAX_RESPONSE_BODY_SIZE = 2000;

let browser: Browser | null = null;
let connecting = false;
let reconnectAttempts = 0;

// ─── Types ───

interface ExecuteRequest {
  id: string;
  code: string;
  timeout_ms?: number;
}

interface SessionCommand {
  id: string;
  command: 'init_session' | 'get_captured_data' | 'close_session';
  params?: Record<string, any>;
}

interface DaemonRequest {
  id: string;
  code?: string;
  command?: string;
  params?: Record<string, any>;
  timeout_ms?: number;
}

interface DaemonResponse {
  id: string;
  success: boolean;
  result?: unknown;
  error?: string;
  stack?: string;
}

interface CapturedRequest {
  id: string;
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: string;
  timestamp: string;
  status?: number;
  responseHeaders: Record<string, string>;
  responseBody?: string;
  responseTimestamp: string;
  durationMs?: number;
}

interface ConsoleEvent {
  type: string;
  event?: string;
  data?: any;
  text?: string;
  timestamp: string;
}

interface PageEvent {
  type: string;
  url?: string;
  title?: string;
  error?: string;
  timestamp: string;
}

interface ResponseMatch {
  value: string;
  type: 'contains' | 'regex';
  invert?: boolean;
}

interface ResponseRedaction {
  xPath?: string;
  jsonPath?: string;
  regex?: string;
  hash?: string;
}

interface RequestDataPattern {
  url: string;
  method: string;
  responseMatches?: ResponseMatch[];
  responseRedactions?: ResponseRedaction[];
  bodySniff?: { enabled: boolean; template?: string };
  responseVariables?: string[];
}

interface ProofResult {
  sessionId: string;
  claim: Record<string, any>;
  signature: Record<string, any>;
  timestamp: string;
  matchedUrl: string;
}

interface ProofEvent {
  type: 'proof_generated' | 'proof_failed' | 'request_matched';
  proof?: ProofResult;
  error?: string;
  matchedUrl?: string;
  timestamp: string;
}

interface SessionConfig {
  sessionId: string;
  injectionType?: 'CDP' | 'HAWKEYE' | 'NONE';
  providerConfig?: Record<string, any>;
  parameters?: Record<string, string>;
  loginUrl?: string;
  customInjection?: string;
  stealthEnabled?: boolean;
}

interface SessionState {
  id: string;
  active: boolean;
  config: SessionConfig;
  capturedRequests: CapturedRequest[];
  consoleEvents: ConsoleEvent[];
  pageEvents: PageEvent[];
  proofEvents: ProofEvent[];
  proofs: ProofResult[];
  listeners: { event: string; fn: (...args: any[]) => void }[];
  provingInProgress: boolean;
}

// ─── Session State ───

let activeSession: SessionState | null = null;

// ─── Code Transformation ───

async function transformCode(code: string): Promise<string> {
  const wrapped = `async function __userCode__() {\n${code}\n}`;
  const result = await transform(wrapped, { loader: 'ts', target: 'es2022' });
  const transformed = result.code;
  const bodyStart = transformed.indexOf('{') + 1;
  const bodyEnd = transformed.lastIndexOf('}');
  if (bodyStart <= 0 || bodyEnd <= bodyStart) return code;
  return transformed.slice(bodyStart, bodyEnd).trim();
}

// ─── Browser Connection ───

async function ensureBrowserConnection(): Promise<Browser> {
  if (browser && browser.isConnected()) return browser;

  if (connecting) {
    while (connecting) await new Promise(resolve => setTimeout(resolve, 50));
    if (browser && browser.isConnected()) return browser;
  }

  connecting = true;
  try {
    const chromium = USE_PATCHRIGHT ? chromiumPR : chromiumPW;
    if (browser) {
      try { await browser.close(); } catch { /* ignore */ }
      browser = null;
    }
    console.error(`[playwright-daemon] Connecting to CDP: ${CDP_ENDPOINT}`);
    browser = await chromium.connectOverCDP(CDP_ENDPOINT);
    reconnectAttempts = 0;
    browser.on('disconnected', () => {
      console.error('[playwright-daemon] Browser disconnected');
      browser = null;
      // Clean up session on disconnect
      if (activeSession) {
        activeSession.active = false;
        activeSession = null;
      }
    });
    console.error('[playwright-daemon] CDP connection established');
    return browser;
  } finally {
    connecting = false;
  }
}

function getContextAndPage(browserInstance: Browser): { context: BrowserContext; page: Page } {
  const contexts = browserInstance.contexts();
  const context = contexts.length > 0 ? contexts[0] : null;
  if (!context) throw new Error('No browser context available');
  const pages = context.pages();
  const page = pages.length > 0 ? pages[0] : null;
  if (!page) throw new Error('No page available');
  return { context, page };
}

// ─── Helper: sanitize response body ───

function sanitizeBody(body: any): string | undefined {
  if (body === undefined || body === null) return undefined;
  if (typeof body === 'string') {
    return body.length > MAX_RESPONSE_BODY_SIZE ? body.slice(0, MAX_RESPONSE_BODY_SIZE) + '...' : body;
  }
  if (typeof body === 'object') {
    try {
      const s = JSON.stringify(body);
      return s.length > MAX_RESPONSE_BODY_SIZE ? s.slice(0, MAX_RESPONSE_BODY_SIZE) + '...' : s;
    } catch { return '[Unserializable body]'; }
  }
  return String(body);
}

// ─── Helper: read script file ───

function readScript(filename: string): string {
  const path = join(SCRIPTS_DIR, filename);
  if (!existsSync(path)) {
    console.error(`[playwright-daemon] Script not found: ${path}`);
    return '';
  }
  return readFileSync(path, 'utf-8');
}

// ─── Response Matching ───

const API_PORT = process.env.KERNEL_IMAGES_API_PORT || '10001';
const DEBUG_DIR = '/tmp/browser-events-debug';

function debugDump(filename: string, data: any): void {
  try {
    mkdirSync(DEBUG_DIR, { recursive: true });
    const path = join(DEBUG_DIR, filename);
    const content = typeof data === 'string' ? data : JSON.stringify(data, null, 2);
    writeFileSync(path, content);
    console.error(`[playwright-daemon] Debug dump: ${path}`);
  } catch (e: any) {
    console.error(`[playwright-daemon] Failed to write debug file: ${e.message}`);
  }
}

function getRequestDataPatterns(config: SessionConfig): RequestDataPattern[] {
  const requestData = config.providerConfig?.requestData;
  if (!Array.isArray(requestData)) return [];
  return requestData;
}

// matchAndBuild calls the Go /browser-events/match endpoint which does:
// 1. XPath/JSONPath extraction using same Go libs as TEE (xpath-go)
// 2. Response matching with template variable substitution
// 3. Building the complete provider_params_json
// All in one call — no round-trips.
async function matchAndBuild(
  captured: { url: string; method: string; headers: Record<string, string>; body?: string },
  responseBody: string,
  pattern: RequestDataPattern,
  parameters: Record<string, string>,
  geoLocation: string,
): Promise<{ matched: boolean; extractedParams: Record<string, string>; providerParamsJson?: string }> {
  try {
    const resp = await fetch(`http://127.0.0.1:${API_PORT}/browser-events/match`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        captured_url: captured.url,
        captured_method: captured.method,
        captured_headers: captured.headers,
        captured_body: captured.body || '',
        response_body: responseBody,
        pattern: {
          url: pattern.url,
          method: pattern.method,
          responseMatches: pattern.responseMatches || [],
          responseRedactions: pattern.responseRedactions || [],
          bodySniff: pattern.bodySniff,
          responseVariables: pattern.responseVariables || [],
        },
        parameters,
        geo_location: geoLocation,
      }),
    });

    if (!resp.ok) {
      console.error(`[playwright-daemon] /browser-events/match failed: ${resp.status}`);
      return { matched: false, extractedParams: {} };
    }

    const result = await resp.json();
    console.error(`[playwright-daemon] Match result: matched=${result.matched}, extracted=${JSON.stringify(result.extracted_params || {})}`);

    return {
      matched: result.matched,
      extractedParams: result.extracted_params || {},
      providerParamsJson: result.provider_params_json,
    };
  } catch (err: any) {
    console.error(`[playwright-daemon] /browser-events/match error: ${err.message}`);
    return { matched: false, extractedParams: {} };
  }
}

// All extraction, matching, and provider params building is done by the Go
// /browser-events/match endpoint using the same xpath-go library as the TEE.

// Trigger proof with pre-built provider_params_json from /browser-events/match
async function triggerProveWithJson(sessionId: string, providerParamsJson: string, matchedUrl: string): Promise<ProofResult> {
  console.error(`[playwright-daemon] Triggering /reclaim/prove for ${matchedUrl}`);

  const resp = await fetch(`http://127.0.0.1:${API_PORT}/reclaim/prove`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      provider_params_json: providerParamsJson,
    }),
  });

  if (!resp.ok) {
    const errorBody = await resp.text();
    throw new Error(`/reclaim/prove returned ${resp.status}: ${errorBody}`);
  }

  const result = await resp.json();
  return {
    sessionId: result.session_id || sessionId,
    claim: result.claim || {},
    signature: result.signature || {},
    timestamp: new Date().toISOString(),
    matchedUrl,
  };
}

// ─── Session Commands ───

async function initSession(params: Record<string, any>): Promise<DaemonResponse> {
  const id = params._requestId || 'init';

  if (activeSession?.active) {
    // Close existing session first
    await closeSession({ _requestId: id });
  }

  try {
    const browserInstance = await ensureBrowserConnection();
    const { context, page } = getContextAndPage(browserInstance);

    const config: SessionConfig = {
      sessionId: params.session_id || `session-${Date.now()}`,
      injectionType: params.injection_type || 'CDP',
      providerConfig: params.provider_config,
      parameters: params.parameters,
      loginUrl: params.login_url,
      customInjection: params.custom_injection,
      stealthEnabled: params.stealth_enabled || false,
    };

    const session: SessionState = {
      id: config.sessionId,
      active: true,
      config,
      capturedRequests: [],
      consoleEvents: [],
      pageEvents: [],
      proofEvents: [],
      proofs: [],
      listeners: [],
      provingInProgress: false,
    };

    // ─── Inject init scripts ───

    // 1. Login script
    const loginScript = readScript('login_script.js');
    if (loginScript) await context.addInitScript({ content: loginScript });

    // 2. Reclaim env
    const reclaimEnvScript = readScript('reclaim_env.js');
    if (reclaimEnvScript) await context.addInitScript({ content: reclaimEnvScript });

    // 3. Provider config injection
    if (config.providerConfig || config.parameters) {
      const reclaimArgs = {
        reclaimProvider: { ...config.providerConfig },
        parameters: config.parameters || {},
      };
      // Remove sensitive/unneeded fields
      for (const key of ['customInjection', 'userAgent', 'viewport', 'proxies']) {
        delete reclaimArgs.reclaimProvider?.[key];
      }
      await context.addInitScript((args: any) => {
        (window as any).Reclaim = (window as any).Reclaim || {};
        (window as any).Reclaim.provider = args.reclaimProvider;
        (window as any).Reclaim.parameters = args.parameters;
      }, reclaimArgs);
    }

    // 4. Injection type specific scripts
    if (config.injectionType === 'HAWKEYE') {
      const hawkeyeScript = readScript('hawkeye.js');
      if (hawkeyeScript) await context.addInitScript({ content: hawkeyeScript });
      await context.addInitScript({
        content: `(() => { try { window.setupHawkeye({ disableFetch: false, disableXHR: false, disableFormIntercept: true, delayFormSubmitForFetch: false, useProxyForFetch: true, useGetterForFetch: false }); } catch(e) { console.error('setupHawkeye failed', e); } })();`,
      });
    } else if (config.injectionType !== 'NONE') {
      // Default CDP flow — install interceptor notifier
      const notifierScript = readScript('interceptor_notifier.js');
      if (notifierScript) await context.addInitScript({ content: notifierScript });
    }

    // 5. Custom injection
    if (config.customInjection) {
      await context.addInitScript({ content: config.customInjection });
    }

    // ─── Set up persistent event listeners ───

    // Network request listener
    const pendingRequests = new Map<string, any>();

    const onRequest = (request: any) => {
      const reqId = `${config.sessionId}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
      pendingRequests.set(request.url() + ':' + request.method(), {
        id: reqId,
        url: request.url(),
        method: request.method(),
        headers: request.headers(),
        body: request.postData() || undefined,
        timestamp: new Date().toISOString(),
      });
    };

    // Pre-compute pattern URL fragments for fast filtering
    const patterns = getRequestDataPatterns(config);
    const patternFilters = patterns.map(p => ({
      pattern: p,
      urlFragment: (p.url || '').replace(/\{\{.*?\}\}/g, '').toLowerCase(),
      method: (p.method || '').toUpperCase(),
    }));

    // Content types to skip (binary/non-text)
    const SKIP_CONTENT_TYPES = /image\/|font\/|video\/|audio\/|application\/octet|application\/wasm|application\/pdf/i;

    const onResponse = async (response: any) => {
      try {
        const request = response.request();
        const key = request.url() + ':' + request.method();
        const reqData = pendingRequests.get(key);
        if (!reqData) return;
        pendingRequests.delete(key);

        const reqUrl = reqData.url.toLowerCase();
        const reqMethod = reqData.method.toUpperCase();

        // Fast filter: only read response body if URL could match a pattern
        const couldMatch = patternFilters.some(f =>
          (!f.method || f.method === reqMethod) &&
          (!f.urlFragment || reqUrl.includes(f.urlFragment))
        );

        // Skip binary content types entirely
        const contentType = response.headers()['content-type'] || '';
        if (SKIP_CONTENT_TYPES.test(contentType)) {
          // Still record the request metadata (no body)
          session.capturedRequests.push({
            id: reqData.id, url: reqData.url, method: reqData.method,
            headers: reqData.headers, timestamp: reqData.timestamp,
            status: response.status(), responseHeaders: response.headers(),
            responseTimestamp: new Date().toISOString(),
          } as CapturedRequest);
          return;
        }

        // Only read full response body if it could match a pattern
        let responseBody: string | undefined;
        if (couldMatch) {
          try {
            responseBody = await response.text();
          } catch {
            return;
          }
        }

        const captured: CapturedRequest = {
          id: reqData.id,
          url: reqData.url,
          method: reqData.method,
          headers: reqData.headers,
          body: sanitizeBody(reqData.body),
          timestamp: reqData.timestamp,
          status: response.status(),
          responseHeaders: response.headers(),
          responseBody: responseBody ? sanitizeBody(responseBody) : undefined,
          responseTimestamp: new Date().toISOString(),
          durationMs: undefined,
        };

        session.capturedRequests.push(captured);

        // ─── Response matching + auto-prove ───
        // Uses Go /browser-events/match endpoint for extraction (same libs as TEE)
        // Only called when URL+method pre-filter matches — typically 1-3 times per session
        if (couldMatch) {
          console.error(`[playwright-daemon] couldMatch=true for ${reqData.method} ${reqData.url} (body: ${responseBody ? responseBody.length : 0} chars, proving: ${session.provingInProgress})`);
        }
        if (!session.provingInProgress && responseBody && couldMatch) {
          const parameters = config.parameters || {};
          const geoLocation = config.providerConfig?.geoLocation || '';

          for (const { pattern } of patternFilters) {
            // Quick method check before making the HTTP call
            if (pattern.method && pattern.method.toUpperCase() !== reqData.method.toUpperCase()) continue;

            const matchResult = await matchAndBuild(
              { url: reqData.url, method: reqData.method, headers: reqData.headers, body: reqData.body },
              responseBody, pattern, parameters, geoLocation,
            );

            if (matchResult.matched && matchResult.providerParamsJson) {
              console.error(`[playwright-daemon] MATCHED: ${reqData.method} ${reqData.url}`);

              // Dump for debugging
              debugDump(`matched-request-${Date.now()}.json`, {
                url: reqData.url, method: reqData.method,
                extractedParams: matchResult.extractedParams,
              });
              debugDump(`provider-params-${Date.now()}.json`, JSON.parse(matchResult.providerParamsJson));

              session.proofEvents.push({
                type: 'request_matched',
                matchedUrl: reqData.url,
                timestamp: new Date().toISOString(),
              });

              // Auto-trigger proof with the pre-built provider_params_json
              session.provingInProgress = true;
              triggerProveWithJson(config.sessionId, matchResult.providerParamsJson, reqData.url)
                .then((proof) => {
                  console.error(`[playwright-daemon] Proof generated for ${reqData.url}`);
                  session.proofs.push(proof);
                  session.proofEvents.push({ type: 'proof_generated', proof, timestamp: new Date().toISOString() });
                })
                .catch((err) => {
                  console.error(`[playwright-daemon] Proof failed: ${err.message}`);
                  session.proofEvents.push({ type: 'proof_failed', error: err.message, matchedUrl: reqData.url, timestamp: new Date().toISOString() });
                })
                .finally(() => { session.provingInProgress = false; });

              break;
            }
          }
        }
      } catch (err: any) {
        // Silently skip errors
      }
    };

    page.on('request', onRequest);
    page.on('response', onResponse);
    session.listeners.push({ event: 'request', fn: onRequest });
    session.listeners.push({ event: 'response', fn: onResponse });

    // Console listener (captures __RECLAIM__ messages)
    const onConsole = (msg: any) => {
      const text = msg.text();
      if (text.startsWith('__RECLAIM__')) {
        try {
          const jsonStr = text.slice('__RECLAIM__'.length);
          const parsed = JSON.parse(jsonStr);
          session.consoleEvents.push({
            type: 'reclaim_api',
            event: parsed.event,
            data: parsed.message,
            timestamp: new Date().toISOString(),
          });
        } catch {
          session.consoleEvents.push({
            type: 'reclaim_raw',
            text,
            timestamp: new Date().toISOString(),
          });
        }
      }
    };

    page.on('console', onConsole);
    session.listeners.push({ event: 'console', fn: onConsole });

    // Page load listener
    const onLoad = async () => {
      try {
        const title = await page.title().catch(() => '');
        session.pageEvents.push({
          type: 'load',
          url: page.url(),
          title,
          timestamp: new Date().toISOString(),
        });
      } catch { /* ignore */ }
    };

    page.on('load', onLoad);
    session.listeners.push({ event: 'load', fn: onLoad });

    // Frame navigation listener
    const onFrameNavigated = (frame: any) => {
      if (frame === page.mainFrame()) {
        session.pageEvents.push({
          type: 'navigation',
          url: frame.url(),
          timestamp: new Date().toISOString(),
        });
      }
    };

    page.on('framenavigated', onFrameNavigated);
    session.listeners.push({ event: 'framenavigated', fn: onFrameNavigated });

    // Page error listener
    const onPageError = (error: any) => {
      const msg = error?.message || String(error);
      // Skip noisy websocket errors
      if (msg.includes('WebSocket')) return;
      session.pageEvents.push({
        type: 'error',
        error: msg,
        timestamp: new Date().toISOString(),
      });
    };

    page.on('pageerror', onPageError);
    session.listeners.push({ event: 'pageerror', fn: onPageError });

    activeSession = session;

    // ─── Navigate to login URL if provided ───
    if (config.loginUrl && config.loginUrl !== 'about:blank') {
      try {
        await page.goto(config.loginUrl, { waitUntil: 'domcontentloaded', timeout: 30000 });
      } catch (err: any) {
        console.error(`[playwright-daemon] Navigation to ${config.loginUrl} failed: ${err.message}`);
      }
    }

    console.error(`[playwright-daemon] Session ${config.sessionId} initialized (injection: ${config.injectionType})`);

    return {
      id,
      success: true,
      result: { sessionId: config.sessionId, injectionType: config.injectionType },
    };
  } catch (err: any) {
    return { id, success: false, error: err.message, stack: err.stack };
  }
}

async function getCapturedData(params: Record<string, any>): Promise<DaemonResponse> {
  const id = params._requestId || 'data';

  if (!activeSession?.active) {
    return { id, success: false, error: 'No active session' };
  }

  // Drain buffers
  const requests = activeSession.capturedRequests.splice(0);
  const consoleEvents = activeSession.consoleEvents.splice(0);
  const pageEvents = activeSession.pageEvents.splice(0);
  const proofEvents = activeSession.proofEvents.splice(0);
  const proofs = [...activeSession.proofs]; // Don't drain proofs — keep them available

  return {
    id,
    success: true,
    result: {
      requests,
      console_events: consoleEvents,
      page_events: pageEvents,
      proof_events: proofEvents,
      proofs,
      proving_in_progress: activeSession.provingInProgress,
    },
  };
}

async function closeSession(params: Record<string, any>): Promise<DaemonResponse> {
  const id = params._requestId || 'close';

  if (!activeSession) {
    return { id, success: true, result: { message: 'No active session' } };
  }

  try {
    // Remove all listeners
    if (browser && browser.isConnected()) {
      try {
        const { page } = getContextAndPage(browser);
        for (const listener of activeSession.listeners) {
          page.removeListener(listener.event, listener.fn);
        }
      } catch { /* page may be gone */ }
    }

    const sessionId = activeSession.id;
    activeSession = null;

    console.error(`[playwright-daemon] Session ${sessionId} closed`);
    return { id, success: true, result: { sessionId } };
  } catch (err: any) {
    activeSession = null;
    return { id, success: false, error: err.message };
  }
}

// ─── Code Execution (original) ───

async function executeCode(request: ExecuteRequest): Promise<DaemonResponse> {
  const { id, code, timeout_ms = 60000 } = request;

  try {
    let jsCode: string;
    try {
      jsCode = await transformCode(code);
    } catch (transformError: any) {
      return { id, success: false, error: `TypeScript transform error: ${transformError.message}`, stack: transformError.stack };
    }

    let browserInstance: Browser;
    try {
      browserInstance = await ensureBrowserConnection();
    } catch (connError: any) {
      reconnectAttempts++;
      if (reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
        return { id, success: false, error: `Failed to connect after ${MAX_RECONNECT_ATTEMPTS} attempts: ${connError.message}` };
      }
      await new Promise(resolve => setTimeout(resolve, RECONNECT_DELAY_MS));
      try {
        browserInstance = await ensureBrowserConnection();
      } catch (retryError: any) {
        return { id, success: false, error: `Failed to connect: ${retryError.message}` };
      }
    }

    const contexts = browserInstance.contexts();
    const context = contexts.length > 0 ? contexts[0] : await browserInstance.newContext();
    const pages = context.pages();
    const page = pages.length > 0 ? pages[0] : await context.newPage();

    const AsyncFunction = Object.getPrototypeOf(async function () { }).constructor;
    const userFunction = new AsyncFunction('page', 'context', 'browser', jsCode);

    const timeoutPromise = new Promise<never>((_, reject) => {
      setTimeout(() => reject(new Error(`Execution timed out after ${timeout_ms}ms`)), timeout_ms);
    });

    const result = await Promise.race([userFunction(page, context, browserInstance), timeoutPromise]);

    return { id, success: true, result: result !== undefined ? result : null };
  } catch (error: any) {
    return { id, success: false, error: error.message, stack: error.stack };
  }
}

// ─── Request Router ───

async function handleRequest(request: DaemonRequest): Promise<DaemonResponse> {
  // Session commands
  if (request.command) {
    const params = { ...(request.params || {}), _requestId: request.id };
    switch (request.command) {
      case 'init_session':
        return initSession(params);
      case 'get_captured_data':
        return getCapturedData(params);
      case 'close_session':
        return closeSession(params);
      default:
        return { id: request.id, success: false, error: `Unknown command: ${request.command}` };
    }
  }

  // Original code execution
  if (typeof request.code === 'string') {
    return executeCode(request as ExecuteRequest);
  }

  return { id: request.id || 'unknown', success: false, error: 'Invalid request: missing code or command' };
}

// ─── Socket Server ───

function handleConnection(socket: Socket): void {
  let buffer = '';

  socket.on('data', async (data) => {
    buffer += data.toString();

    let newlineIndex: number;
    while ((newlineIndex = buffer.indexOf('\n')) !== -1) {
      const line = buffer.slice(0, newlineIndex);
      buffer = buffer.slice(newlineIndex + 1);

      if (!line.trim()) continue;

      let request: DaemonRequest;
      try {
        request = JSON.parse(line);
      } catch {
        socket.write(JSON.stringify({ id: 'unknown', success: false, error: 'Invalid JSON request' }) + '\n');
        continue;
      }

      if (!request.id) {
        socket.write(JSON.stringify({ id: 'unknown', success: false, error: 'Missing request id' }) + '\n');
        continue;
      }

      const response = await handleRequest(request);
      socket.write(JSON.stringify(response) + '\n');
    }
  });

  socket.on('error', (err) => {
    console.error('[playwright-daemon] Socket error:', err.message);
  });
}

async function shutdown(signal: string): Promise<void> {
  console.error(`[playwright-daemon] Received ${signal}, shutting down...`);

  if (activeSession) {
    await closeSession({ _requestId: 'shutdown' });
  }

  if (browser) {
    try { await browser.close(); } catch { /* ignore */ }
  }

  try {
    if (existsSync(SOCKET_PATH)) unlinkSync(SOCKET_PATH);
  } catch { /* ignore */ }

  process.exit(0);
}

async function main(): Promise<void> {
  try {
    if (existsSync(SOCKET_PATH)) unlinkSync(SOCKET_PATH);
  } catch { /* ignore */ }

  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));

  const server = createServer(handleConnection);

  server.on('error', (err) => {
    console.error('[playwright-daemon] Server error:', err);
    process.exit(1);
  });

  server.listen(SOCKET_PATH, () => {
    console.error(`[playwright-daemon] Listening on ${SOCKET_PATH}`);
    ensureBrowserConnection().catch((err) => {
      console.error('[playwright-daemon] Initial connection failed:', err.message);
    });
  });
}

main().catch((err) => {
  console.error('[playwright-daemon] Fatal error:', err);
  process.exit(1);
});
