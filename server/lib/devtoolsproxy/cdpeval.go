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
	ElementX      float64 `json:"x,omitempty"`
	ElementY      float64 `json:"y,omitempty"`

	// SelectInfo is populated when the focused element is a <select>. Carries
	// the options + bounding rect so the popcorn page can postMessage a
	// POPCORN_SHOW_SELECT to the embedding portal (which renders its own
	// dropdown UI in place of Chromium's native, stream-invisible one).
	SelectInfo *SelectInfo `json:"selectInfo,omitempty"`
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
  const el = getDeep(document);
  if (!el) return { error: 'null' };
  if (el === document.body) return { error: 'body' };
  if (el.isCrossoriginIframe) return { isCrossoriginIframe: true };

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

  return {
    tagName: (el.tagName || '').toLowerCase(),
    type: (el.type || '').toLowerCase(),
    isEditable: el.isContentEditable,
    readonly: !!el.readOnly,
    disabled: !!el.disabled,
    focusKey: focusKeyFor(el),
    rawOuterHTML: el.outerHTML ? el.outerHTML.substring(0, 250) : '',
    elementTop: rect ? rect.top : 0,
    elementHeight: rect ? rect.height : 0,
    x: rect ? rect.left + rect.width / 2 : 0,
    y: rect ? rect.top + rect.height / 2 : 0,
    selectInfo: selectInfo
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
}

// NewFocusTracker creates and starts a FocusTracker. Call Stop() to shut it down.
func NewFocusTracker(mgr *UpstreamManager, logger *slog.Logger) *FocusTracker {
	ctx, cancel := context.WithCancel(context.Background())
	ft := &FocusTracker{
		mgr:          mgr,
		logger:       logger,
		cancel:       cancel,
		pollInterval: 100 * time.Millisecond,
	}
	// Seed with a safe default
	ft.cached.Store(&ActiveElementResult{IsInput: false, Tag: "initializing"})
	go ft.run(ctx)
	return ft
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

	// Step 3: poll loop — evaluate document.activeElement repeatedly
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(ft.pollInterval):
		}

		id := nextID()
		evalParams, _ := json.Marshal(map[string]interface{}{
			"expression":    activeElementExpression,
			"returnByValue": true,
		})
		if err := send(cdpMsg{ID: id, Method: "Runtime.evaluate", Params: evalParams, SessionID: sessionID}); err != nil {
			ft.logger.Warn("[FocusTracker] evaluate send failed, reconnecting", "err", err)
			return
		}

		// Read until we get our response (skip events)
		for {
			m, err := recv()
			if err != nil {
				ft.logger.Warn("[FocusTracker] evaluate recv failed, reconnecting", "err", err)
				return
			}
			if m.ID != id {
				continue // skip CDP events (e.g. Target.attachedToTarget)
			}

			result := parseEvalResult(m.Result)
			ft.cached.Store(result)
			break
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

	if errStr, ok := val["error"].(string); ok {
		return &ActiveElementResult{IsInput: false, Tag: errStr}
	}
	if val["isCrossoriginIframe"] == true {
		return &ActiveElementResult{IsInput: true, Tag: "iframe"}
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
		ElementX:      elementX,
		ElementY:      elementY,
		SelectInfo:    selectInfo,
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
