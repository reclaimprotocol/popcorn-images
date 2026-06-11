package devtoolsproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// PopupWatcher makes window.open popups usable inside the kiosk stream while
// creating ZERO debugger sessions of its own — important because popups are
// overwhelmingly auth/SSO flows on anti-bot login pages.
//
// Stealth-first design:
//   - We only DISCOVER targets (Target.setDiscoverTargets), never attach. Auto-
//     attach (Target.setAutoAttach) would attach a debugger session to EVERY
//     target and, worse, create new targets paused in a "waiting for debugger"
//     state, greying out the opener with a "Debugger paused in another tab"
//     banner. Discovery has neither effect.
//   - Fullscreen + close use browser-level commands (Browser.getWindowForTarget
//     / setWindowBounds / closeTarget) that take only a targetId — no session.
//
// For each discovered popup (a page target with a non-empty openerId) we:
//  1. Fullscreen its window → hides the location bar / origin chip that --kiosk
//     leaves on non-primary windows (no flag removes it).
//  2. Record the popup's targetId on the UpstreamManager. That has two effects:
//     the client shows an in-app close button (a chromeless fullscreen popup
//     has no native close), and the FocusTracker + InputDispatcher re-target the
//     popup (they watch InputTargetGen) — so focus tracking, the soft keyboard,
//     device emulation, and keystrokes all follow the popup while it's open, and
//     snap back to the main page when it closes. We deliberately do NOT attach
//     here: those existing components already own emulation/focus/input on
//     whichever target is active, so adopting the popup needs no new session.
type PopupWatcher struct {
	mgr    *UpstreamManager
	logger *slog.Logger
	cancel context.CancelFunc
}

// NewPopupWatcher starts the watcher in the background. Call Stop() to shut down.
func NewPopupWatcher(mgr *UpstreamManager, logger *slog.Logger) *PopupWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	pw := &PopupWatcher{mgr: mgr, logger: logger, cancel: cancel}
	go pw.run(ctx)
	return pw
}

// Stop terminates the watcher goroutine and its CDP connection.
func (pw *PopupWatcher) Stop() { pw.cancel() }

func (pw *PopupWatcher) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		pw.watchLoop(ctx)
		// watchLoop returned → connection died or upstream not ready. Back off
		// then reconnect (target discovery is re-issued on reconnect).
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// watchLoop opens a browser-level CDP connection, enables target discovery, and
// fullscreens + records popups as they appear until the connection dies.
func (pw *PopupWatcher) watchLoop(ctx context.Context) {
	upstream := pw.mgr.Current()
	if upstream == "" {
		pw.logger.Warn("[PopupWatcher] upstream not ready, waiting...")
		return
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	conn, _, err := websocket.Dial(connCtx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		pw.logger.Warn("[PopupWatcher] dial failed", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(10 * 1024 * 1024)

	var idMu sync.Mutex
	var msgID int
	nextID := func() int {
		idMu.Lock()
		defer idMu.Unlock()
		msgID++
		return msgID
	}

	var writeMu sync.Mutex
	send := func(msg cdpMsg) error {
		b, _ := json.Marshal(msg)
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(connCtx, websocket.MessageText, b)
	}

	// Pending response correlation: a reader goroutine delivers each command
	// response to the channel registered under its id. Events (id==0) are
	// dispatched by method.
	var pendingMu sync.Mutex
	pending := make(map[int]chan *cdpMsg)
	request := func(method string, params any) (*cdpMsg, error) {
		id := nextID()
		ch := make(chan *cdpMsg, 1)
		pendingMu.Lock()
		pending[id] = ch
		pendingMu.Unlock()
		defer func() {
			pendingMu.Lock()
			delete(pending, id)
			pendingMu.Unlock()
		}()
		if err := send(cdpMsg{ID: id, Method: method, Params: mustJSON(params)}); err != nil {
			return nil, err
		}
		select {
		case <-connCtx.Done():
			return nil, connCtx.Err()
		case m := <-ch:
			return m, nil
		}
	}

	recv := func() (*cdpMsg, error) {
		_, b, err := conn.Read(connCtx)
		if err != nil {
			return nil, err
		}
		var m cdpMsg
		return &m, json.Unmarshal(b, &m)
	}

	// fullscreenPopup hides the popup window's location bar (browser-level, no
	// session) and records it as active. Runs in its own goroutine because it
	// issues a request() whose response is delivered by the reader loop.
	fullscreenPopup := func(targetID string) {
		if winResp, err := request("Browser.getWindowForTarget", map[string]any{"targetId": targetID}); err == nil && winResp != nil && winResp.Result != nil {
			var wr struct {
				WindowID int `json:"windowId"`
			}
			if json.Unmarshal(winResp.Result, &wr) == nil && wr.WindowID != 0 {
				_ = send(cdpMsg{
					ID:     nextID(),
					Method: "Browser.setWindowBounds",
					Params: mustJSON(map[string]any{
						"windowId": wr.WindowID,
						"bounds":   map[string]any{"windowState": "fullscreen"},
					}),
				})
			}
		} else if err != nil {
			pw.logger.Warn("[PopupWatcher] getWindowForTarget failed", "err", err, "target", targetID)
		}
		// Recording the popup re-targets FocusTracker + InputDispatcher (they
		// emulate/track/type on it) and shows the client's close button.
		pw.mgr.SetActivePopup(targetID)
		pw.logger.Info("[PopupWatcher] popup adopted", "target", targetID)
	}

	// Reader goroutine: deliver responses, dispatch events.
	readerErr := make(chan error, 1)
	go func() {
		for {
			m, err := recv()
			if err != nil {
				readerErr <- err
				return
			}
			if m.ID != 0 {
				pendingMu.Lock()
				ch := pending[m.ID]
				pendingMu.Unlock()
				if ch != nil {
					ch <- m
				}
				continue
			}
			switch m.Method {
			case "Target.targetCreated":
				var p struct {
					TargetInfo struct {
						TargetID string `json:"targetId"`
						Type     string `json:"type"`
						OpenerID string `json:"openerId"`
					} `json:"targetInfo"`
				}
				if json.Unmarshal(m.Params, &p) != nil {
					continue
				}
				// Only page popups (have an opener). The main page, workers,
				// iframes, etc. have no openerId and are left untouched.
				if p.TargetInfo.Type == "page" && p.TargetInfo.OpenerID != "" {
					go fullscreenPopup(p.TargetInfo.TargetID)
				}
			case "Target.targetDestroyed":
				var p struct {
					TargetID string `json:"targetId"`
				}
				if json.Unmarshal(m.Params, &p) == nil && p.TargetID != "" &&
					p.TargetID == pw.mgr.ActivePopup() {
					pw.mgr.SetActivePopup("")
				}
			}
		}
	}()

	// Enable discovery — fires targetCreated/targetDestroyed without attaching
	// to anything (so nothing is created paused).
	if err := send(cdpMsg{ID: nextID(), Method: "Target.setDiscoverTargets", Params: mustJSON(map[string]any{"discover": true})}); err != nil {
		pw.logger.Warn("[PopupWatcher] setDiscoverTargets failed", "err", err)
		return
	}

	pw.logger.Info("[PopupWatcher] watching for popups")

	// Block until the connection dies or we're cancelled.
	select {
	case <-ctx.Done():
	case err := <-readerErr:
		pw.logger.Warn("[PopupWatcher] reader exited, reconnecting", "err", err)
	}
}

// mustJSON marshals v to json.RawMessage, returning an empty object on error.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// ClosePopupHandler returns an http.Handler for POST /cdp/close-popup. It
// closes the currently tracked popup target (Target.closeTarget). Used by the
// client's in-app close button, since fullscreen popups have no browser chrome.
func ClosePopupHandler(mgr *UpstreamManager, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetID := mgr.ActivePopup()
		if targetID == "" {
			// Nothing open — treat as a no-op success so the client can clear
			// its button state without special-casing.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"closed":false}`))
			return
		}

		upstream := mgr.Current()
		if upstream == "" {
			http.Error(w, "CDP upstream not ready", http.StatusServiceUnavailable)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := closeTargetCDP(ctx, upstream, targetID); err != nil {
			logger.Warn("[ClosePopup] failed", "err", err, "target", targetID)
			http.Error(w, "close-popup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Optimistically clear; the watcher also clears on targetDestroyed.
		mgr.SetActivePopup("")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"closed":true}`))
	})
}

// closeTargetCDP opens a one-shot browser-level CDP connection and closes the
// given target. Target.closeTarget is a browser-level method (no session needed).
func closeTargetCDP(ctx context.Context, upstream, targetID string) error {
	conn, _, err := websocket.Dial(ctx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	id := 1
	b, _ := json.Marshal(cdpMsg{ID: id, Method: "Target.closeTarget", Params: mustJSON(map[string]any{"targetId": targetID})})
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	// Wait for the matching response (ignore unrelated events).
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var m cdpMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID == id {
			return nil
		}
	}
}
