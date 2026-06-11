package devtoolsproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// ActiveElementResult holds the result of a CDP active-element check.
type ActiveElementResult struct {
	IsInput      bool   `json:"isInput"`
	Tag          string `json:"tag"`
	Type         string `json:"type,omitempty"`
	IsEditable   bool   `json:"isEditable,omitempty"`
	RawOuterHTML string `json:"rawOuterHTML,omitempty"`

	// Extra fields used by the popcorn client's Android auto-focus poller.
	// Readonly/Disabled gate keyboard auto-pop. FocusKey is a stable
	// per-element identifier so the poller can remember "user explicitly
	// dismissed the keyboard on this exact field" — it stays suppressed
	// until focus moves to a different element.
	Readonly bool   `json:"readonly,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	FocusKey string `json:"focusKey,omitempty"`

	// Focused-element bounding box (CSS pixels, relative to the viewport).
	// Used by the keyboard-aware lift to compute how much to translate the
	// streamed video element. Omitted (all zero) for non-input focuses or
	// when getBoundingClientRect() fails.
	ElementTop    float64 `json:"elementTop,omitempty"`
	ElementHeight float64 `json:"elementHeight,omitempty"`
	ElementLeft   float64 `json:"elementLeft,omitempty"`
	ElementWidth  float64 `json:"elementWidth,omitempty"`
	ElementX      float64 `json:"x,omitempty"`
	ElementY      float64 `json:"y,omitempty"`

	// SelectInfo is populated when the focused element is a <select>. Carries
	// the options + bounding rect so the popcorn page can postMessage a
	// POPCORN_SHOW_SELECT to the embedding portal (which renders its own
	// dropdown UI in place of Chromium's native, stream-invisible one).
	SelectInfo *SelectInfo `json:"selectInfo,omitempty"`

	// InputRects is the bounding-box list of every visible text-input-like
	// element in the page's layout viewport (CSS pixels). The client caches
	// this from each focus push and uses it for synchronous tap-time hit
	// tests — at 3 s RTT, a per-tap CDP round-trip is too slow, but
	// matching against a 600 ms-fresh cache is O(N) local work and
	// network-independent. Tap inside any rect → input → pop. Tap outside
	// all rects → non-input → dismiss.
	InputRects []SelectRect `json:"inputRects,omitempty"`

	// ViewportWidth / ViewportHeight is chromium's current layout viewport
	// in CSS pixels. Pushed so the client can transform tap coords into
	// the SAME space rects are reported in — important because cdp-magnify
	// (or any out-of-band Emulation.setDeviceMetricsOverride) changes this
	// size and the client otherwise has no way to know.
	ViewportWidth  float64 `json:"viewportWidth,omitempty"`
	ViewportHeight float64 `json:"viewportHeight,omitempty"`

	// Hints that drive the LOCAL proxy input's attributes so the
	// platform IME (Gboard, Samsung Keyboard, iOS QuickType) shows the
	// correct layout. Native pages get a numeric pad for type=number,
	// email layout for type=email, etc. — without these, the proxy is
	// just type=text and every field gets the same QWERTY.
	//
	// Mirrors the HTML attribute names so the client just does:
	//   proxy.type = info.inputType
	//   proxy.inputMode = info.inputMode
	//   proxy.autocomplete = info.autocomplete
	//   proxy.enterKeyHint = info.enterKeyHint
	InputType     string `json:"inputType,omitempty"`
	InputMode     string `json:"inputMode,omitempty"`
	AutoComplete  string `json:"autoComplete,omitempty"`
	EnterKeyHint  string `json:"enterKeyHint,omitempty"`
}

// SelectInfo describes a focused <select> element. The shape matches the
// `POPCORN_SHOW_SELECT` postMessage payload documented in MOBILE_INPUT.md.
type SelectInfo struct {
	Multiple bool           `json:"multiple"`
	Rect     SelectRect     `json:"rect"`
	Options  []SelectOption `json:"options"`
}

type SelectRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type SelectOption struct {
	Value      string `json:"value"`
	Text       string `json:"text"`
	Selected   bool   `json:"selected"`
	Disabled   bool   `json:"disabled"`
	GroupLabel string `json:"groupLabel,omitempty"`
}

const activeElementExpression = `
(() => {
  function getDeep(doc) {
    let a = doc.activeElement;
    if (!a) return null;
    if (a.shadowRoot) return getDeep(a.shadowRoot) || a;
    if (a.tagName && a.tagName.toLowerCase() === 'iframe') {
      try { if (a.contentDocument) return getDeep(a.contentDocument) || a; }
      catch (_) { return { isCrossoriginIframe: true }; }
    }
    return a;
  }

  // Collect viewport + input rects FIRST — these are independent of which
  // element happens to be focused, and the client needs them regardless
  // (the input-rects cache drives tap-time hit-testing whether or not an
  // input is currently focused, and viewport dims drive coord transforms).
  var viewportWidth = document.documentElement ? document.documentElement.clientWidth : 0;
  var viewportHeight = document.documentElement ? document.documentElement.clientHeight : 0;
  // True iff a tap at the input's center actually lands on it — i.e. nothing
  // is painted on top. Without this an input occluded by an overlay (mobile
  // nav drawer, modal, cookie banner) stays in the cache, and the client's
  // geometric hit-test can't see z-order, so a tap on the *overlay* (e.g. a
  // nav button sitting over a full-width search field) wrongly pops the soft
  // keyboard. elementFromPoint returns the topmost painted node, so it gives
  // us the z-order the rect list otherwise loses. Also drops off-screen rects
  // (center clamped into the viewport won't resolve back to the input).
  function isReachable(n, r) {
    var cx = Math.min(Math.max(r.left + r.width / 2, 0), viewportWidth - 1);
    var cy = Math.min(Math.max(r.top + r.height / 2, 0), viewportHeight - 1);
    var top = null;
    try { top = document.elementFromPoint(cx, cy); } catch (_) { return true; }
    if (!top) return false;
    if (top === n || n.contains(top)) return true;
    // Tolerate overlay children of the same control (e.g. a search box's
    // magnifier icon absolutely positioned over the field center): climb a
    // few ancestors of the hit looking for the input itself.
    var a = top;
    for (var k = 0; a && k < 5; k++) { if (a === n) return true; a = a.parentNode; }
    return false;
  }
  var inputRects = [];
  try {
    var sel = 'input, textarea, [contenteditable=""], [contenteditable="true"]';
    var nodes = document.querySelectorAll(sel);
    for (var i = 0; i < nodes.length && inputRects.length < 100; i++) {
      var n = nodes[i];
      var t = (n.tagName || '').toLowerCase();
      if (t === 'input') {
        var tt = (n.type || '').toLowerCase();
        if (tt && tt !== 'text' && tt !== 'email' && tt !== 'password' &&
            tt !== 'search' && tt !== 'tel' && tt !== 'url' &&
            tt !== 'number' && tt !== 'date' && tt !== 'datetime-local' &&
            tt !== 'time' && tt !== 'month' && tt !== 'week') continue;
      }
      if (n.disabled || n.readOnly) continue;
      // Exclude non-typeable combobox-style inputs — they look like a
      // dropdown button (e.g. Kaggle's "Relevance" sort) but our
      // selector picks them up because they're <input type=text>
      // underneath. Tap should open the popup, not the soft keyboard.
      //
      // Distinguishing signal: a typeable autocomplete (like a search bar
      // that shows suggestions) declares aria-autocomplete=list/both/inline.
      // A non-typeable combobox sets role=combobox / aria-haspopup but
      // omits aria-autocomplete (or sets it to "none"). Walk up a few
      // ancestors so wrapper-divs with role=combobox also count.
      var skipPopup = false;
      var cur = n;
      var typeable = false;
      // Check the input itself for aria-autocomplete — only the input
      // node knows whether the user types into it.
      if (n.getAttribute) {
        var selfAC = n.getAttribute('aria-autocomplete') || '';
        if (selfAC === 'list' || selfAC === 'both' || selfAC === 'inline') typeable = true;
      }
      if (!typeable) {
        for (var d = 0; cur && d < 4; d++) {
          if (cur.getAttribute) {
            var role = cur.getAttribute('role') || '';
            var pop = cur.getAttribute('aria-haspopup') || '';
            if (role === 'combobox' || role === 'listbox' || role === 'menu') { skipPopup = true; break; }
            if (pop && pop !== 'false') { skipPopup = true; break; }
          }
          cur = cur.parentNode;
        }
      }
      if (skipPopup) continue;
      var r = null;
      try { r = n.getBoundingClientRect(); } catch (_) {}
      if (!r || r.width < 4 || r.height < 4) continue;
      // Skip inputs hidden behind an overlay (open nav drawer, modal) or
      // scrolled off-screen — tapping their screen coords doesn't reach them.
      if (!isReachable(n, r)) continue;
      inputRects.push({ x: r.left, y: r.top, width: r.width, height: r.height });
    }
  } catch (_) { /* DOM unavailable */ }

  const el = getDeep(document);
  if (!el) return { error: 'null', viewportWidth: viewportWidth, viewportHeight: viewportHeight, inputRects: inputRects };
  if (el === document.body) return { error: 'body', viewportWidth: viewportWidth, viewportHeight: viewportHeight, inputRects: inputRects };
  if (el.isCrossoriginIframe) return { isCrossoriginIframe: true, viewportWidth: viewportWidth, viewportHeight: viewportHeight, inputRects: inputRects };

  // Stable per-element key. Combines DOM position (parent chain indices) with
  // identifying attributes — same element across re-renders gets the same key.
  function focusKeyFor(node) {
    var parts = [];
    var n = node;
    while (n && n !== document.documentElement && parts.length < 12) {
      var idx = 0;
      var sib = n.parentNode ? n.parentNode.firstChild : null;
      while (sib && sib !== n) { idx++; sib = sib.nextSibling; }
      parts.push((n.nodeName || '?') + ':' + idx);
      n = n.parentNode;
    }
    var tagPart = (node.tagName || '').toLowerCase();
    var idPart = node.id ? '#' + node.id : '';
    var namePart = node.name ? '@' + node.name : '';
    var typePart = node.type ? ':' + node.type : '';
    return tagPart + idPart + namePart + typePart + '|' + parts.join('>');
  }

  var rect = null;
  try { rect = el.getBoundingClientRect(); } catch (_) {}

  // If a <select> is focused, collect its options + rect so the popcorn
  // page can postMessage POPCORN_SHOW_SELECT to the portal in place of
  // letting Chromium open its native (stream-invisible) dropdown.
  var selectInfo = null;
  if (el.tagName && el.tagName.toLowerCase() === 'select' && el.options) {
    var opts = [];
    var children = el.children || [];
    for (var i = 0; i < children.length; i++) {
      var c = children[i];
      if (!c.tagName) continue;
      var tag = c.tagName.toLowerCase();
      if (tag === 'optgroup') {
        var label = c.label || '';
        var inner = c.children || [];
        for (var j = 0; j < inner.length; j++) {
          var o = inner[j];
          if (o.tagName && o.tagName.toLowerCase() === 'option') {
            opts.push({
              value: o.value, text: o.text || '',
              selected: !!o.selected, disabled: !!o.disabled,
              groupLabel: label
            });
          }
        }
      } else if (tag === 'option') {
        opts.push({
          value: c.value, text: c.text || '',
          selected: !!c.selected, disabled: !!c.disabled,
          groupLabel: ''
        });
      }
    }
    selectInfo = {
      multiple: !!el.multiple,
      rect: rect ? { x: rect.left, y: rect.top, width: rect.width, height: rect.height } : { x: 0, y: 0, width: 0, height: 0 },
      options: opts
    };
  }

  // Pull the IME-shaping attributes off the focused element. inputmode
  // (HTML spec: numeric, email, tel, decimal, url, search) overrides
  // the type-derived default; modern sites set it explicitly. Fall back
  // to type/autocomplete heuristics if the page didn't set inputmode.
  function attrOr(node, name, dflt) {
    try { var v = node.getAttribute ? node.getAttribute(name) : null;
          return (v == null || v === '') ? dflt : v; } catch (_) { return dflt; }
  }
  var inputType = (el.type || '').toLowerCase();
  var inputMode = (attrOr(el, 'inputmode', '') || '').toLowerCase();
  var autoComplete = attrOr(el, 'autocomplete', '');
  var enterKeyHint = attrOr(el, 'enterkeyhint', '');

  return {
    tagName: (el.tagName || '').toLowerCase(),
    type: inputType,
    isEditable: el.isContentEditable,
    readonly: !!el.readOnly,
    disabled: !!el.disabled,
    focusKey: focusKeyFor(el),
    rawOuterHTML: el.outerHTML ? el.outerHTML.substring(0, 250) : '',
    elementTop: rect ? rect.top : 0,
    elementHeight: rect ? rect.height : 0,
    elementLeft: rect ? rect.left : 0,
    elementWidth: rect ? rect.width : 0,
    x: rect ? rect.left + rect.width / 2 : 0,
    y: rect ? rect.top + rect.height / 2 : 0,
    selectInfo: selectInfo,
    inputRects: inputRects,
    viewportWidth: viewportWidth,
    viewportHeight: viewportHeight,
    inputType: inputType,
    inputMode: inputMode,
    autoComplete: autoComplete,
    enterKeyHint: enterKeyHint
  };
})()
`

type cdpMsg struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// ---------------------------------------------------------------------------
// FocusTracker: persistent CDP session that polls document.activeElement
// and caches the result. Reads are lock-free via atomic.Pointer.
// ---------------------------------------------------------------------------

// FocusTracker maintains a persistent CDP connection and polls the active
// element at a configurable interval. The cached result can be read without
// any I/O via CurrentState().
type FocusTracker struct {
	mgr    *UpstreamManager
	logger *slog.Logger

	cached atomic.Pointer[ActiveElementResult]
	cancel context.CancelFunc

	// pollInterval controls how often we re-evaluate the active element.
	pollInterval time.Duration

	// dialogResp carries a user's (or auto-) response to a pending JS dialog
	// into the poll loop, which sends Page.handleJavaScriptDialog on the live
	// session. Buffered+drop because dialogs are modal (one at a time).
	dialogResp chan dialogResponse
}

// dialogResponse is how the dialog is dismissed: accept/cancel plus optional
// prompt text. Fed by RespondDialog (client) or auto-accept (beforeunload).
type dialogResponse struct {
	accept     bool
	promptText string
}

// NewFocusTracker creates and starts a FocusTracker. Call Stop() to shut it down.
func NewFocusTracker(mgr *UpstreamManager, logger *slog.Logger) *FocusTracker {
	ctx, cancel := context.WithCancel(context.Background())
	ft := &FocusTracker{
		mgr:          mgr,
		logger:       logger,
		cancel:       cancel,
		pollInterval: 100 * time.Millisecond,
		dialogResp:   make(chan dialogResponse, 1),
	}
	// Seed with a safe default
	ft.cached.Store(&ActiveElementResult{IsInput: false, Tag: "initializing"})
	go ft.run(ctx)
	return ft
}

// RespondDialog dismisses the pending JS dialog with the user's choice. Called
// by the /cdp/dialog-respond handler; the poll loop performs the actual CDP
// call on the session that owns the dialog.
func (ft *FocusTracker) RespondDialog(accept bool, promptText string) {
	select {
	case ft.dialogResp <- dialogResponse{accept: accept, promptText: promptText}:
	default: // a response is already queued — dialogs are modal, so drop extras
	}
}

// CurrentState returns the most recently cached active-element state.
// This is an atomic pointer read — effectively zero latency.
func (ft *FocusTracker) CurrentState() *ActiveElementResult {
	return ft.cached.Load()
}

// Stop terminates the background goroutine and CDP connection.
func (ft *FocusTracker) Stop() {
	ft.cancel()
}

func (ft *FocusTracker) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ft.pollLoop(ctx)
		// If pollLoop returns, the connection died. Wait and reconnect.
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pollLoop establishes a persistent CDP connection to the browser, attaches
// to the page target, and polls Runtime.evaluate in a tight loop.
func (ft *FocusTracker) pollLoop(ctx context.Context) {
	upstream := ft.mgr.Current()
	if upstream == "" {
		ft.logger.Warn("[FocusTracker] upstream not ready, waiting...")
		return
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	conn, _, err := websocket.Dial(connCtx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		ft.logger.Warn("[FocusTracker] dial failed", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(10 * 1024 * 1024)

	var msgID int
	var mu sync.Mutex

	send := func(msg cdpMsg) error {
		b, _ := json.Marshal(msg)
		mu.Lock()
		defer mu.Unlock()
		return conn.Write(connCtx, websocket.MessageText, b)
	}

	recv := func() (*cdpMsg, error) {
		_, b, err := conn.Read(connCtx)
		if err != nil {
			return nil, err
		}
		var m cdpMsg
		return &m, json.Unmarshal(b, &m)
	}

	nextID := func() int {
		msgID++
		return msgID
	}

	// Step 1: find the page target
	id1 := nextID()
	if err := send(cdpMsg{ID: id1, Method: "Target.getTargets", Params: json.RawMessage(`{}`)}); err != nil {
		ft.logger.Warn("[FocusTracker] getTargets send failed", "err", err)
		return
	}

	var targetID string
	for {
		m, err := recv()
		if err != nil {
			ft.logger.Warn("[FocusTracker] getTargets recv failed", "err", err)
			return
		}
		if m.ID != id1 {
			continue
		}
		var res struct {
			TargetInfos []struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				URL      string `json:"url"`
			} `json:"targetInfos"`
		}
		if err := json.Unmarshal(m.Result, &res); err != nil {
			ft.logger.Warn("[FocusTracker] getTargets parse failed", "err", err)
			return
		}
		for _, t := range res.TargetInfos {
			if t.Type == "page" && !strings.HasPrefix(t.URL, "devtools://") {
				targetID = t.TargetID
				break
			}
		}
		break
	}

	// Prefer the active popup so focus tracking (and thus the soft keyboard +
	// input rects) follow an open popup window, e.g. an OAuth login. Snapshot
	// the generation first so that if the popup opens/closes after this point
	// the ticker loop notices and reconnects to the new target.
	attachGen := ft.mgr.InputTargetGen()
	if popup := ft.mgr.ActivePopup(); popup != "" {
		targetID = popup
	}

	if targetID == "" {
		ft.cached.Store(&ActiveElementResult{IsInput: false, Tag: "no-page-target"})
		return
	}

	// Step 2: attach to the page target
	id2 := nextID()
	attachParams, _ := json.Marshal(map[string]interface{}{"targetId": targetID, "flatten": true})
	if err := send(cdpMsg{ID: id2, Method: "Target.attachToTarget", Params: attachParams}); err != nil {
		ft.logger.Warn("[FocusTracker] attachToTarget send failed", "err", err)
		return
	}

	var sessionID string
	for {
		m, err := recv()
		if err != nil {
			ft.logger.Warn("[FocusTracker] attachToTarget recv failed", "err", err)
			return
		}
		if m.ID == id2 {
			var res struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Result, &res)
			sessionID = res.SessionID
			break
		}
	}

	if sessionID == "" {
		ft.logger.Warn("[FocusTracker] no sessionId returned")
		return
	}

	ft.logger.Info("[FocusTracker] connected, starting poll loop", "targetId", targetID, "sessionId", sessionID)

	// Step 3: enable ONLY the Page domain. We deliberately do NOT call
	// Runtime.enable or install a Runtime.addBinding-based focus listener.
	// Runtime.enable is the canonical CDP-detection signal: anti-bot pages
	// (reCAPTCHA, Cloudflare, DataDome) detect it via the console-argument
	// serialization leak — a getter on a console-logged object fires only when
	// a debugger is serializing consoleAPICalled events — which tanks the bot
	// score and causes "reCAPTCHA score too low"/challenge failures. Page.enable
	// has no such leak and is needed for navigation re-emulation (loadEventFired)
	// and JS-dialog interception, so we keep it. Focus / input-rect changes are
	// picked up by the ~100 ms Runtime.evaluate poll below (Runtime.evaluate does
	// NOT require Runtime.enable) — slightly higher latency than an event push,
	// but no detectable debugger footprint.
	_ = send(cdpMsg{ID: nextID(), Method: "Page.enable", Params: json.RawMessage(`{}`), SessionID: sessionID})

	// Re-apply the device emulation on this long-lived session. CDP's
	// Emulation overrides are cleared when the setting session detaches, so the
	// one-shot /cdp/emulate-device apply does not survive navigations — but this
	// session stays attached for the page's lifetime, so applying here keeps the
	// mobile viewport across page loads, redirects, and our own reconnects.
	applyEmulation := func() {
		for _, c := range ft.mgr.EmulationCommands() {
			params, _ := json.Marshal(c.Params)
			_ = send(cdpMsg{ID: nextID(), Method: c.Method, Params: params, SessionID: sessionID})
		}
	}
	applyEmulation()
	lastEmuVersion := ft.mgr.EmulationVersion()

	// Step 4: poll loop. Re-evaluates the active element every pollInterval
	// (~100 ms). There is no event-driven push anymore — that required
	// Runtime.enable + a binding, which is a CDP-detection signal (see Step 3).
	// Signalled by the reader when the top document finishes loading
	// (Page.loadEventFired) — a navigation just wiped the renderer's
	// emulation state, so re-apply it.
	emulateReapply := make(chan struct{}, 1)
	requestReapply := func() {
		select {
		case emulateReapply <- struct{}{}:
		default: // already pending
		}
	}

	// Goroutine reads CDP events and triggers an eval whenever the
	// binding fires. Runs until the connection dies.
	readerDone := make(chan error, 1)
	pendingEvalID := 0
	var pendingMu sync.Mutex
	go func() {
		for {
			m, err := recv()
			if err != nil {
				readerDone <- err
				return
			}
			// Handle our own eval responses.
			if m.ID != 0 {
				pendingMu.Lock()
				want := pendingEvalID
				pendingMu.Unlock()
				if m.ID == want {
					result := parseEvalResult(m.Result)
					ft.cached.Store(result)
				}
				continue
			}
			// A top-document load cleared the renderer's emulation overrides;
			// re-apply them so the mobile viewport survives the navigation.
			if m.Method == "Page.loadEventFired" {
				requestReapply()
			}
			// A JS dialog opened. The native dialog can't render in the emulated
			// crop and is suppressed by Page.enable, so we surface it to the
			// client (which draws its own overlay) — except beforeunload, which
			// is kiosk noise and auto-accepts. Responding is done on the main
			// loop (here we only signal) so we never race the msgID counter.
			if m.Method == "Page.javascriptDialogOpening" {
				var p struct {
					Type          string `json:"type"`
					Message       string `json:"message"`
					DefaultPrompt string `json:"defaultPrompt"`
				}
				if json.Unmarshal(m.Params, &p) == nil {
					if p.Type == "beforeunload" {
						select {
						case ft.dialogResp <- dialogResponse{accept: true}:
						default:
						}
					} else {
						ft.mgr.SetPendingDialog(&DialogInfo{
							ID:            ft.mgr.NextDialogID(),
							Kind:          p.Type,
							Message:       p.Message,
							DefaultPrompt: p.DefaultPrompt,
						})
					}
				}
			}
		}
	}()

	// Driver loop: triggers Runtime.evaluate on either a tick or a
	// binding event. ID coordination via pendingMu so the reader
	// recognizes our response.
	ticker := time.NewTicker(ft.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-emulateReapply:
			applyEmulation()
		case resp := <-ft.dialogResp:
			// Dismiss the pending JS dialog on the session that owns it.
			dialogParams, _ := json.Marshal(map[string]interface{}{
				"accept":     resp.accept,
				"promptText": resp.promptText,
			})
			_ = send(cdpMsg{ID: nextID(), Method: "Page.handleJavaScriptDialog", Params: dialogParams, SessionID: sessionID})
			ft.mgr.SetPendingDialog(nil)
		}

		// A popup opened or closed — reconnect so we re-attach to the now-active
		// target (popup while open, main page once it closes).
		if ft.mgr.InputTargetGen() != attachGen {
			ft.logger.Info("[FocusTracker] input target changed, reconnecting")
			return
		}

		// A new /cdp/emulate-device call bumped the version — apply the fresh
		// config. (Navigation re-applies via emulateReapply above.)
		if v := ft.mgr.EmulationVersion(); v != lastEmuVersion {
			lastEmuVersion = v
			applyEmulation()
		}

		id := nextID()
		pendingMu.Lock()
		pendingEvalID = id
		pendingMu.Unlock()

		evalParams, _ := json.Marshal(map[string]interface{}{
			"expression":    activeElementExpression,
			"returnByValue": true,
		})
		if err := send(cdpMsg{ID: id, Method: "Runtime.evaluate", Params: evalParams, SessionID: sessionID}); err != nil {
			ft.logger.Warn("[FocusTracker] evaluate send failed, reconnecting", "err", err)
			return
		}

		// Bail out promptly if the reader goroutine reports the
		// connection died (no point looping on a dead socket).
		select {
		case err := <-readerDone:
			ft.logger.Warn("[FocusTracker] reader exited, reconnecting", "err", err)
			return
		default:
		}
	}
}

// parseEvalResult converts a CDP Runtime.evaluate response into an ActiveElementResult.
func parseEvalResult(raw json.RawMessage) *ActiveElementResult {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return &ActiveElementResult{IsInput: false, Tag: "parse-error"}
	}
	var val map[string]interface{}
	if err := json.Unmarshal(res.Result.Value, &val); err != nil {
		return &ActiveElementResult{IsInput: false, Tag: "parse-error"}
	}

	// Pull viewport + inputRects out before checking the early-exit paths
	// (body / null activeElement / cross-origin iframe). These fields are
	// independent of focus state, and the client needs them on every push.
	viewportWidthEarly, _ := val["viewportWidth"].(float64)
	viewportHeightEarly, _ := val["viewportHeight"].(float64)
	var inputRectsEarly []SelectRect
	if ir, ok := val["inputRects"].([]interface{}); ok && len(ir) > 0 {
		if b, err := json.Marshal(ir); err == nil {
			_ = json.Unmarshal(b, &inputRectsEarly)
		}
	}

	if errStr, ok := val["error"].(string); ok {
		return &ActiveElementResult{
			IsInput: false, Tag: errStr,
			InputRects:     inputRectsEarly,
			ViewportWidth:  viewportWidthEarly,
			ViewportHeight: viewportHeightEarly,
		}
	}
	if val["isCrossoriginIframe"] == true {
		return &ActiveElementResult{
			IsInput: true, Tag: "iframe",
			InputRects:     inputRectsEarly,
			ViewportWidth:  viewportWidthEarly,
			ViewportHeight: viewportHeightEarly,
		}
	}

	tag, _ := val["tagName"].(string)
	typ, _ := val["type"].(string)
	isEditable, _ := val["isEditable"].(bool)
	readonly, _ := val["readonly"].(bool)
	disabled, _ := val["disabled"].(bool)
	focusKey, _ := val["focusKey"].(string)
	rawHTML, _ := val["rawOuterHTML"].(string)
	elementTop, _ := val["elementTop"].(float64)
	elementHeight, _ := val["elementHeight"].(float64)
	elementLeft, _ := val["elementLeft"].(float64)
	elementWidth, _ := val["elementWidth"].(float64)
	elementX, _ := val["x"].(float64)
	elementY, _ := val["y"].(float64)

	isInputTag := tag == "input" || tag == "textarea"
	isTextType := typ == "" || typ == "text" || typ == "email" || typ == "password" ||
		typ == "search" || typ == "tel" || typ == "url" || typ == "number"
	isInput := (isInputTag && isTextType) || isEditable || tag == "textarea"

	// selectInfo is heterogeneous JSON — round-trip via Marshal/Unmarshal to
	// land in the typed SelectInfo struct without enumerating fields manually.
	var selectInfo *SelectInfo
	if si, ok := val["selectInfo"].(map[string]interface{}); ok && si != nil {
		if b, err := json.Marshal(si); err == nil {
			var parsed SelectInfo
			if json.Unmarshal(b, &parsed) == nil {
				selectInfo = &parsed
			}
		}
	}

	// Reuse the focus-independent fields we already extracted at the top.
	inputRects := inputRectsEarly
	viewportWidth := viewportWidthEarly
	viewportHeight := viewportHeightEarly

	inputType, _ := val["inputType"].(string)
	inputMode, _ := val["inputMode"].(string)
	autoComplete, _ := val["autoComplete"].(string)
	enterKeyHint, _ := val["enterKeyHint"].(string)

	return &ActiveElementResult{
		IsInput:       isInput,
		Tag:           tag,
		Type:          typ,
		IsEditable:    isEditable,
		Readonly:      readonly,
		Disabled:      disabled,
		FocusKey:      focusKey,
		RawOuterHTML:  rawHTML,
		ElementTop:    elementTop,
		ElementHeight: elementHeight,
		ElementLeft:   elementLeft,
		ElementWidth:  elementWidth,
		ElementX:      elementX,
		ElementY:      elementY,
		SelectInfo:     selectInfo,
		InputRects:     inputRects,
		ViewportWidth:  viewportWidth,
		ViewportHeight: viewportHeight,
		InputType:      inputType,
		InputMode:      inputMode,
		AutoComplete:   autoComplete,
		EnterKeyHint:   enterKeyHint,
	}
}

// ---------------------------------------------------------------------------
// HTTP Handler: reads cached state from FocusTracker (near-zero latency)
// ---------------------------------------------------------------------------

// ActiveElementHandler returns an http.Handler that reads the cached
// active-element state from a FocusTracker and responds with JSON.
func ActiveElementHandler(ft *FocusTracker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := ft.CurrentState()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

// DialogRespondHandler returns an http.Handler for POST /cdp/dialog-respond.
// The client's dialog overlay posts {id, accept, promptText}; we dismiss the
// pending JS dialog via the FocusTracker's session. The id guards against a
// stale response (from a dialog the user already answered) dismissing a newer one.
func DialogRespondHandler(ft *FocusTracker, mgr *UpstreamManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID         uint64 `json:"id"`
			Accept     bool   `json:"accept"`
			PromptText string `json:"promptText"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		cur := mgr.PendingDialog()
		if cur == nil || (body.ID != 0 && body.ID != cur.ID) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"stale":true}`))
			return
		}
		ft.RespondDialog(body.Accept, body.PromptText)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}
