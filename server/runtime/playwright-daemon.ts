/*
 * Persistent Playwright executor with browser-events session support.
 *
 * Sits on a Unix socket. Accepts two kinds of requests:
 *
 *   Code execution (original):
 *     { id, code, timeout_ms? }  →  { id, success, result?, error? }
 *
 *   Session commands (browser-events):
 *     { id, command: "init_session",     params }  →  sets up listeners + scripts
 *     { id, command: "get_captured_data", params }  →  drains captured network/events
 *     { id, command: "close_session",    params }  →  tears down listeners
 *
 * The daemon maintains one warm CDP connection and one active session.
 * Network requests are captured, matched against provider patterns via the Go
 * /browser-events/match endpoint, and proofs are auto-triggered via /reclaim/prove.
 */

import { createServer, Socket } from 'net';
import { unlinkSync, existsSync, readFileSync, writeFileSync, mkdirSync } from 'fs';
import { join } from 'path';
import { transform } from 'esbuild';
import { chromium as chromiumPW, Browser, Page, BrowserContext } from 'playwright-core';
import { chromium as chromiumPR } from 'patchright';

const SOCKET = process.env.PLAYWRIGHT_DAEMON_SOCKET || '/tmp/playwright-daemon.sock';
const CDP_URL = process.env.CDP_ENDPOINT || 'ws://127.0.0.1:9222';
const API_PORT = process.env.KERNEL_IMAGES_API_PORT || '10001';
const SCRIPTS_DIR = process.env.BROWSER_EVENTS_SCRIPTS_DIR || '/usr/local/lib/browser-events-scripts';
const DEBUG_DIR = '/tmp/browser-events-debug';
const PATCHRIGHT = process.env.PLAYWRIGHT_ENGINE !== 'playwright-core';
const BODY_LIMIT = 2000;

interface Request { id: string; code?: string; command?: string; params?: any; timeout_ms?: number }
interface Response { id: string; success: boolean; result?: any; error?: string; stack?: string }

interface CapturedReq {
  id: string; url: string; method: string; headers: Record<string, string>;
  body?: string; timestamp: string; status?: number;
  responseHeaders?: Record<string, string>; responseBody?: string;
  responseTimestamp?: string;
}

interface Session {
  id: string;
  active: boolean;
  config: SessionConfig;
  requests: CapturedReq[];
  consoleEvents: any[];
  pageEvents: any[];
  proofEvents: any[];
  proofs: any[];
  listeners: { event: string; fn: (...a: any[]) => void }[];
  proving: boolean;
}

interface SessionConfig {
  sessionId: string;
  injectionType?: string;
  providerConfig?: any;
  parameters?: Record<string, string>;
  loginUrl?: string;
  customInjection?: string;
}

let browser: Browser | null = null;
let connecting = false;
let reconnects = 0;
let session: Session | null = null;

async function connect(): Promise<Browser> {
  if (browser?.isConnected()) return browser;

  while (connecting) await sleep(50);
  if (browser?.isConnected()) return browser;

  connecting = true;
  try {
    if (browser) { try { await browser.close(); } catch { } }
    browser = null;

    log(`connecting to ${CDP_URL}`);
    const chromium = PATCHRIGHT ? chromiumPR : chromiumPW;
    browser = await chromium.connectOverCDP(CDP_URL);
    reconnects = 0;

    browser.on('disconnected', () => {
      log('browser disconnected');
      browser = null;
      if (session) { session.active = false; session = null; }
    });

    log('connected');
    return browser;
  } finally {
    connecting = false;
  }
}

function getPage(b: Browser): { ctx: BrowserContext; page: Page } {
  const ctx = b.contexts()[0];
  if (!ctx) throw new Error('no browser context');
  const page = ctx.pages()[0];
  if (!page) throw new Error('no page');
  return { ctx, page };
}

async function execCode(req: Request): Promise<Response> {
  const { id, code = '', timeout_ms = 60000 } = req;

  let js: string;
  try { js = await compileTS(code); }
  catch (e: any) { return { id, success: false, error: `transform: ${e.message}`, stack: e.stack }; }

  let b: Browser;
  try { b = await connect(); }
  catch (e: any) {
    if (++reconnects >= 10) return { id, success: false, error: `connect failed after 10 attempts` };
    await sleep(1000);
    try { b = await connect(); }
    catch (e2: any) { return { id, success: false, error: `connect: ${e2.message}` }; }
  }

  const ctx = b.contexts()[0] || await b.newContext();
  const page = ctx.pages()[0] || await ctx.newPage();
  const run = new (Object.getPrototypeOf(async () => { }).constructor)('page', 'context', 'browser', js);

  try {
    const result = await Promise.race([
      run(page, ctx, b),
      sleep(timeout_ms).then(() => { throw new Error(`timeout after ${timeout_ms}ms`); }),
    ]);
    return { id, success: true, result: result ?? null };
  } catch (e: any) {
    return { id, success: false, error: e.message, stack: e.stack };
  }
}

async function compileTS(code: string): Promise<string> {
  const wrapped = `async function __f__() {\n${code}\n}`;
  const out = await transform(wrapped, { loader: 'ts', target: 'es2022' });
  const s = out.code;
  const a = s.indexOf('{') + 1;
  const b = s.lastIndexOf('}');
  return (a > 0 && b > a) ? s.slice(a, b).trim() : code;
}


async function initSession(p: any): Promise<Response> {
  const id = p._requestId;
  if (session?.active) await closeSession({ _requestId: id });

  try {
    const b = await connect();
    const { ctx, page } = getPage(b);

    const cfg: SessionConfig = {
      sessionId: p.session_id || `s-${Date.now()}`,
      injectionType: p.injection_type || 'CDP',
      providerConfig: p.provider_config,
      parameters: p.parameters,
      loginUrl: p.login_url,
      customInjection: p.custom_injection,
    };

    session = {
      id: cfg.sessionId, active: true, config: cfg,
      requests: [], consoleEvents: [], pageEvents: [],
      proofEvents: [], proofs: [], listeners: [], proving: false,
    };

    await injectScripts(ctx, cfg);
    attachListeners(page, session);

    if (cfg.loginUrl && cfg.loginUrl !== 'about:blank') {
      try { await page.goto(cfg.loginUrl, { waitUntil: 'domcontentloaded', timeout: 30000 }); }
      catch (e: any) { log(`nav failed: ${e.message}`); }
    }

    log(`session ${cfg.sessionId} ready (${cfg.injectionType})`);
    return { id, success: true, result: { sessionId: cfg.sessionId, injectionType: cfg.injectionType } };
  } catch (e: any) {
    return { id, success: false, error: e.message, stack: e.stack };
  }
}

async function getData(p: any): Promise<Response> {
  const id = p._requestId;
  if (!session?.active) return { id, success: false, error: 'no active session' };

  return {
    id, success: true,
    result: {
      requests: session.requests.splice(0),
      console_events: session.consoleEvents.splice(0),
      page_events: session.pageEvents.splice(0),
      proof_events: session.proofEvents.splice(0),
      proofs: [...session.proofs],
      proving_in_progress: session.proving,
    },
  };
}


async function closeSession(p: any): Promise<Response> {
  const id = p._requestId;
  if (!session) return { id, success: true, result: { message: 'no session' } };

  if (browser?.isConnected()) {
    try {
      const { page } = getPage(browser);
      for (const l of session.listeners) page.removeListener(l.event, l.fn);
    } catch { }
  }

  const sid = session.id;
  session = null;
  log(`session ${sid} closed`);
  return { id, success: true, result: { sessionId: sid } };
}


async function injectScripts(ctx: BrowserContext, cfg: SessionConfig) {
  await addScript(ctx, 'login_script.js');
  await addScript(ctx, 'reclaim_env.js');

  if (cfg.providerConfig || cfg.parameters) {
    const args = {
      reclaimProvider: stripKeys(cfg.providerConfig || {}, ['customInjection', 'userAgent', 'viewport', 'proxies']),
      parameters: cfg.parameters || {},
    };
    await ctx.addInitScript((a: any) => {
      (window as any).Reclaim = (window as any).Reclaim || {};
      (window as any).Reclaim.provider = a.reclaimProvider;
      (window as any).Reclaim.parameters = a.parameters;
    }, args);
  }

  switch (cfg.injectionType) {
    case 'HAWKEYE':
      await addScript(ctx, 'hawkeye.js');
      await ctx.addInitScript({ content: `(() => { try { window.setupHawkeye({ disableFetch:false, disableXHR:false, disableFormIntercept:true, delayFormSubmitForFetch:false, useProxyForFetch:true, useGetterForFetch:false }); } catch(e) {} })();` });
      break;
    case 'NONE':
      break;
    default:
      await addScript(ctx, 'interceptor_notifier.js');
  }

  if (cfg.customInjection) {
    await ctx.addInitScript({ content: cfg.customInjection });
  }
}

async function addScript(ctx: BrowserContext, file: string) {
  const path = join(SCRIPTS_DIR, file);
  if (!existsSync(path)) return;
  await ctx.addInitScript({ content: readFileSync(path, 'utf-8') });
}


function attachListeners(page: Page, s: Session) {
  const cfg = s.config;
  const pending = new Map<string, any>();
  const patterns = buildPatternFilters(cfg);
  const skipBinary = /image\/|font\/|video\/|audio\/|application\/octet|application\/wasm|application\/pdf/i;

  /* request — stash metadata for later matching against response */
  const onReq = (r: any) => {
    pending.set(r.url() + ':' + r.method(), {
      id: `${cfg.sessionId}-${Date.now()}-${rand(6)}`,
      url: r.url(), method: r.method(), headers: r.headers(),
      body: r.postData() || undefined, timestamp: now(),
    });
  };

  /* response — match, extract, prove */
  const onRes = async (res: any) => {
    try {
      const req = res.request();
      const key = req.url() + ':' + req.method();
      const meta = pending.get(key);
      if (!meta) return;
      pending.delete(key);

      const ct = res.headers()['content-type'] || '';
      if (skipBinary.test(ct)) {
        s.requests.push({ ...meta, status: res.status(), responseTimestamp: now() });
        return;
      }

      const couldMatch = patterns.some(f =>
        (!f.method || f.method === meta.method.toUpperCase()) &&
        (!f.frag || meta.url.toLowerCase().includes(f.frag))
      );

      let body: string | undefined;
      if (couldMatch) {
        try { body = await res.text(); } catch { return; }
      }

      s.requests.push({
        ...meta, status: res.status(),
        responseHeaders: res.headers(),
        responseBody: body ? truncate(body, BODY_LIMIT) : undefined,
        responseTimestamp: now(),
      });

      if (!s.proving && body && couldMatch) {
        await tryMatch(s, meta, body, patterns);
      }
    } catch { }
  };

  /* console — capture __RECLAIM__ messages from injected scripts */
  const onConsole = (msg: any) => {
    const text = msg.text();
    if (!text.startsWith('__RECLAIM__')) return;
    try {
      const { event, message } = JSON.parse(text.slice(11));
      s.consoleEvents.push({ type: 'reclaim_api', event, data: message, timestamp: now() });
    } catch {
      s.consoleEvents.push({ type: 'reclaim_raw', text, timestamp: now() });
    }
  };

  /* page lifecycle */
  const onLoad = async () => {
    try { s.pageEvents.push({ type: 'load', url: page.url(), title: await page.title().catch(() => ''), timestamp: now() }); } catch { }
  };
  const onNav = (f: any) => {
    if (f === page.mainFrame()) s.pageEvents.push({ type: 'navigation', url: f.url(), timestamp: now() });
  };
  const onErr = (e: any) => {
    const msg = e?.message || String(e);
    if (!msg.includes('WebSocket')) s.pageEvents.push({ type: 'error', error: msg, timestamp: now() });
  };

  listen(page, s, 'request', onReq);
  listen(page, s, 'response', onRes);
  listen(page, s, 'console', onConsole);
  listen(page, s, 'load', onLoad);
  listen(page, s, 'framenavigated', onNav);
  listen(page, s, 'pageerror', onErr);
}

function listen(page: Page, s: Session, event: string, fn: (...a: any[]) => void) {
  page.on(event, fn);
  s.listeners.push({ event, fn });
}

interface PatternFilter { pattern: any; frag: string; method: string }

function buildPatternFilters(cfg: SessionConfig): PatternFilter[] {
  const data = cfg.providerConfig?.requestData;
  if (!Array.isArray(data)) return [];
  return data.map((p: any) => ({
    pattern: p,
    frag: (p.url || '').replace(/\{\{.*?\}\}/g, '').toLowerCase(),
    method: (p.method || '').toUpperCase(),
  }));
}

async function tryMatch(s: Session, meta: any, body: string, filters: PatternFilter[]) {
  const params = s.config.parameters || {};
  const geo = s.config.providerConfig?.geoLocation || '';

  for (const { pattern } of filters) {
    if (pattern.method && pattern.method.toUpperCase() !== meta.method.toUpperCase()) continue;

    const result = await callMatch(meta, body, pattern, params, geo);
    if (!result.matched || !result.provider_params_json) continue;

    log(`MATCHED ${meta.method} ${meta.url}`);
    dump(`matched-${Date.now()}.json`, { url: meta.url, params: result.extracted_params });
    dump(`prove-${Date.now()}.json`, JSON.parse(result.provider_params_json));

    s.proofEvents.push({ type: 'request_matched', matchedUrl: meta.url, timestamp: now() });

    s.proving = true;
    prove(s.config.sessionId, result.provider_params_json, meta.url)
      .then(proof => {
        log(`proof OK for ${meta.url}`);
        s.proofs.push(proof);
        s.proofEvents.push({ type: 'proof_generated', proof, timestamp: now() });
      })
      .catch(err => {
        log(`proof FAIL: ${err.message}`);
        s.proofEvents.push({ type: 'proof_failed', error: err.message, matchedUrl: meta.url, timestamp: now() });
      })
      .finally(() => { s.proving = false; });

    break;
  }
}

async function callMatch(meta: any, body: string, pattern: any, params: Record<string, string>, geo: string) {
  try {
    const r = await fetch(`http://127.0.0.1:${API_PORT}/browser-events/match`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        captured_url: meta.url,
        captured_method: meta.method,
        captured_headers: meta.headers,
        captured_body: meta.body || '',
        response_body: body,
        pattern: {
          url: pattern.url, method: pattern.method,
          responseMatches: pattern.responseMatches || [],
          responseRedactions: pattern.responseRedactions || [],
          bodySniff: pattern.bodySniff,
          responseVariables: pattern.responseVariables || [],
        },
        parameters: params,
        geo_location: geo,
      }),
    });
    return r.ok ? await r.json() : { matched: false };
  } catch {
    return { matched: false };
  }
}

async function prove(sessionId: string, paramsJson: string, url: string) {
  const r = await fetch(`http://127.0.0.1:${API_PORT}/reclaim/prove`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider_params_json: paramsJson }),
  });
  if (!r.ok) throw new Error(`/reclaim/prove ${r.status}: ${await r.text()}`);
  const data = await r.json();
  return {
    sessionId: data.session_id || sessionId,
    claim: data.claim || {}, signature: data.signature || {},
    timestamp: now(), matchedUrl: url,
  };
}

function route(req: Request): Promise<Response> {
  if (req.command) {
    const p = { ...(req.params || {}), _requestId: req.id };
    switch (req.command) {
      case 'init_session': return initSession(p);
      case 'get_captured_data': return getData(p);
      case 'close_session': return closeSession(p);
      default: return ok(req.id, false, undefined, `unknown command: ${req.command}`);
    }
  }
  if (typeof req.code === 'string') return execCode(req);
  return ok(req.id || '?', false, undefined, 'missing code or command');
}

function handleConn(sock: Socket) {
  let buf = '';
  sock.on('data', async (d: Buffer) => {
    buf += d.toString();
    let nl: number;
    while ((nl = buf.indexOf('\n')) !== -1) {
      const line = buf.slice(0, nl); buf = buf.slice(nl + 1);
      if (!line.trim()) continue;
      let req: Request;
      try { req = JSON.parse(line); } catch { sock.write(JSON.stringify({ id: '?', success: false, error: 'bad json' }) + '\n'); continue; }
      if (!req.id) { sock.write(JSON.stringify({ id: '?', success: false, error: 'no id' }) + '\n'); continue; }
      sock.write(JSON.stringify(await route(req)) + '\n');
    }
  });
  sock.on('error', (e: any) => log(`socket error: ${e.message}`));
}


function log(msg: string) { console.error(`[playwright-daemon] ${msg}`); }
function now() { return new Date().toISOString(); }
function sleep(ms: number) { return new Promise(r => setTimeout(r, ms)); }
function rand(n: number) { return Math.random().toString(36).slice(2, 2 + n); }

function truncate(s: string, max: number) {
  return s.length > max ? s.slice(0, max) + '...' : s;
}

function stripKeys(obj: any, keys: string[]) {
  const out = { ...obj };
  for (const k of keys) delete out[k];
  return out;
}

function ok(id: string, success: boolean, result?: any, error?: string): Promise<Response> {
  return Promise.resolve({ id, success, result, error });
}

function dump(file: string, data: any) {
  try {
    mkdirSync(DEBUG_DIR, { recursive: true });
    writeFileSync(join(DEBUG_DIR, file), JSON.stringify(data, null, 2));
  } catch { }
}

async function shutdown(sig: string) {
  log(`${sig}, shutting down`);
  if (session) await closeSession({ _requestId: 'shutdown' });
  if (browser) { try { await browser.close(); } catch { } }
  try { if (existsSync(SOCKET)) unlinkSync(SOCKET); } catch { }
  process.exit(0);
}

async function main() {
  try { if (existsSync(SOCKET)) unlinkSync(SOCKET); } catch { }
  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));

  createServer(handleConn).listen(SOCKET, () => {
    log(`listening on ${SOCKET}`);
    connect().catch(e => log(`initial connect failed: ${e.message}`));
  });
}

main().catch(e => { log(`fatal: ${e}`); process.exit(1); });
