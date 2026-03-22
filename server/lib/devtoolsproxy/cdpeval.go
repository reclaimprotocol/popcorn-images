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
  return {
    tagName: (el.tagName || '').toLowerCase(),
    type: (el.type || '').toLowerCase(),
    isEditable: el.isContentEditable,
    rawOuterHTML: el.outerHTML ? el.outerHTML.substring(0, 250) : ''
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

	// Shared CDP connection for input commands (used by CDPInputHandler)
	mu        sync.RWMutex
	conn      *websocket.Conn
	sessionID string
	connMu    sync.Mutex // serializes writes to conn
	commandID int32      // atomic counter for command IDs
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

func (ft *FocusTracker) nextCommandID() int {
	return int(atomic.AddInt32(&ft.commandID, 1))
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

	cdpConn, _, err := websocket.Dial(connCtx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		ft.logger.Warn("[FocusTracker] dial failed", "err", err)
		return
	}
	defer func() {
		cdpConn.Close(websocket.StatusNormalClosure, "")
		ft.mu.Lock()
		ft.conn = nil
		ft.sessionID = ""
		ft.mu.Unlock()
	}()
	cdpConn.SetReadLimit(10 * 1024 * 1024)

	// Share the connection for CDPInputHandler
	ft.mu.Lock()
	ft.conn = cdpConn
	ft.mu.Unlock()

	var msgID int
	conn := cdpConn

	send := func(msg cdpMsg) error {
		b, _ := json.Marshal(msg)
		ft.connMu.Lock()
		defer ft.connMu.Unlock()
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

	// Share session ID for CDPInputHandler
	ft.mu.Lock()
	ft.sessionID = sessionID
	ft.mu.Unlock()

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
	rawHTML, _ := val["rawOuterHTML"].(string)

	isInputTag := tag == "input" || tag == "textarea"
	isTextType := typ == "" || typ == "text" || typ == "email" || typ == "password" ||
		typ == "search" || typ == "tel" || typ == "url" || typ == "number"
	isInput := (isInputTag && isTextType) || isEditable || tag == "textarea"

	return &ActiveElementResult{
		IsInput:      isInput,
		Tag:          tag,
		Type:         typ,
		IsEditable:   isEditable,
		RawOuterHTML: rawHTML,
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

// CDPInputRequest represents a CDP command sent from the client via HTTP.
type CDPInputRequest struct {
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId,omitempty"`
}

// CDPInputHandler returns an http.Handler that accepts CDP commands via POST
// and sends them through the FocusTracker's CDP connection. This avoids the
// client needing its own WebSocket (Chrome limits concurrent DevTools connections).
func CDPInputHandler(ft *FocusTracker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req CDPInputRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		ft.mu.Lock()
		conn := ft.conn
		sessionID := ft.sessionID
		ft.mu.Unlock()

		if conn == nil {
			http.Error(w, "CDP not connected", http.StatusServiceUnavailable)
			return
		}

		// Use the FocusTracker's session ID if client didn't provide one
		sid := req.SessionID
		if sid == "" {
			sid = sessionID
		}

		msg := cdpMsg{
			ID:        ft.nextCommandID(),
			Method:    req.Method,
			Params:    req.Params,
			SessionID: sid,
		}

		b, _ := json.Marshal(msg)
		ft.connMu.Lock()
		err := conn.Write(r.Context(), websocket.MessageText, b)
		ft.connMu.Unlock()

		if err != nil {
			http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
}
