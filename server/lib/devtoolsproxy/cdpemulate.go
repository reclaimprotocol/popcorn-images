package devtoolsproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// EmulateDeviceRequest is the JSON body of POST /cdp/emulate-device. It maps
// 1:1 onto a small subset of CDP Emulation.* parameters chosen to make the
// remote page render as a mobile viewport while the underlying Xorg
// framebuffer stays at its native resolution.
//
// Width/Height are the layout viewport in CSS pixels (e.g. 390x844 for
// iPhone 14). Scale upscales the rendered output to fill the chromium
// window — set it to framebufferWidth/Width so the mobile page fills the
// streamed frame instead of rendering into a small corner.
type EmulateDeviceRequest struct {
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	Mobile            bool    `json:"mobile"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor,omitempty"`
	Scale             float64 `json:"scale,omitempty"`
	UserAgent         string  `json:"userAgent,omitempty"`
	Touch             bool    `json:"touch"`
	MaxTouchPoints    int     `json:"maxTouchPoints,omitempty"`
}

// SetSelectValueRequest is the JSON body of POST /cdp/set-select-value.
// Carries the values the user picked in the portal's custom dropdown UI;
// the server applies them to the currently focused <select> via CDP
// Runtime.evaluate, then dispatches `input` + `change` events so the
// remote page's React/Vue handlers fire.
type SetSelectValueRequest struct {
	Values []string `json:"values"`
}

// SetSelectValueHandler returns an http.Handler that applies user-picked
// dropdown values to the remote chromium's focused <select>. Used by the
// popcorn ↔ portal select-dropdown bridge: the portal posts PORTAL_SET_-
// SELECT_VALUE → popcorn Vue → POST here → CDP Runtime.evaluate.
//
// We do this server-side (instead of letting the Vue client send
// Runtime.evaluate directly over the WSS) because Runtime.evaluate is
// deliberately *not* in the WebSocket proxy allowlist — that would let any
// caller reachable on :9222 execute arbitrary JS in the page. The unfiltered
// upstream CDP socket is intra-server only.
func SetSelectValueHandler(mgr *UpstreamManager, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req SetSelectValueRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}

		upstream := mgr.Current()
		if upstream == "" {
			http.Error(w, "CDP upstream not ready", http.StatusServiceUnavailable)
			return
		}

		valuesJSON, _ := json.Marshal(req.Values)
		expression := `
		(() => {
		  const a = document.activeElement;
		  if (!a || !a.tagName || a.tagName.toLowerCase() !== 'select') return false;
		  const wanted = ` + string(valuesJSON) + `;
		  if (a.multiple) {
		    for (const opt of a.options) opt.selected = wanted.includes(opt.value);
		  } else if (wanted.length > 0) {
		    a.value = wanted[0];
		  }
		  a.dispatchEvent(new Event('input', { bubbles: true }));
		  a.dispatchEvent(new Event('change', { bubbles: true }));
		  return true;
		})()
		`

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := runCDPCommands(ctx, upstream, logger, []cdpCommand{
			{
				Method: "Runtime.evaluate",
				Params: map[string]any{"expression": expression, "returnByValue": true},
			},
		}); err != nil {
			logger.Warn("[SetSelectValue] failed", "err", err)
			http.Error(w, "set-select-value failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

// EmulateDeviceHandler returns an http.Handler that opens a one-shot CDP
// session to the upstream chromium and applies the requested Emulation.*
// overrides. Used by the neko Vue client to switch the page into mobile
// layout on connect from a touch device, without resizing the framebuffer.
func EmulateDeviceHandler(mgr *UpstreamManager, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req EmulateDeviceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Width <= 0 || req.Height <= 0 {
			http.Error(w, "width and height must be > 0", http.StatusBadRequest)
			return
		}
		// CDP requires maxTouchPoints in [1,16] even when disabling touch
		// (enabled:false) — sending 0 errors with "Touch points must be between
		// 1 and 16". Default to 5 when touch is on, and clamp to ≥1 always.
		if req.MaxTouchPoints == 0 && req.Touch {
			req.MaxTouchPoints = 5
		}
		if req.MaxTouchPoints < 1 {
			req.MaxTouchPoints = 1
		}
		if req.MaxTouchPoints > 16 {
			req.MaxTouchPoints = 16
		}

		upstream := mgr.Current()
		if upstream == "" {
			http.Error(w, "CDP upstream not ready", http.StatusServiceUnavailable)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		// CDP's setDeviceMetricsOverride defaults deviceScaleFactor and scale to
		// 1. Sending 0 (the zero value when the client omits them) mis-scales
		// the render — the page lays out at the right viewport but paints at the
		// wrong size, so the client crop shows only a fraction of it. Default
		// both to 1 and only include scale when explicitly set (>0), matching
		// the known-good cdp-magnify.sh which omits scale entirely.
		dsf := req.DeviceScaleFactor
		if dsf <= 0 {
			dsf = 1
		}
		metrics := map[string]any{
			"width":             req.Width,
			"height":            req.Height,
			"deviceScaleFactor": dsf,
			"mobile":            req.Mobile,
			"screenWidth":       req.Width,
			"screenHeight":      req.Height,
		}
		if req.Scale > 0 {
			metrics["scale"] = req.Scale
		}
		commands := []cdpCommand{
			{
				Method: "Emulation.setDeviceMetricsOverride",
				Params: metrics,
			},
			{
				Method: "Emulation.setTouchEmulationEnabled",
				Params: map[string]any{
					"enabled":        req.Touch,
					"maxTouchPoints": req.MaxTouchPoints,
				},
			},
		}
		if req.UserAgent != "" {
			commands = append(commands, cdpCommand{
				Method: "Emulation.setUserAgentOverride",
				Params: map[string]any{"userAgent": req.UserAgent},
			})
		}

		if err := runCDPCommands(ctx, upstream, logger, commands); err != nil {
			logger.Warn("[EmulateDevice] failed", "err", err)
			http.Error(w, "emulation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Record touch state so /computer/scroll can pick a touch-drag swipe
		// over wheel ticks without probing the page on every scroll.
		mgr.SetTouchEmulated(req.Touch)

		// Persist the command set so the FocusTracker's long-lived session
		// re-applies it on every navigation. The one-shot apply above is
		// cleared when this session detaches (CDP Emulation overrides are
		// session-scoped); without re-application a hard navigation or auth
		// redirect snaps the page back to its desktop layout inside the crop.
		mgr.SetEmulationCommands(commands)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

// cdpCommand is a single CDP method+params pair to be run inside an attached
// page session by runCDPCommands.
type cdpCommand struct {
	Method string
	Params map[string]any
}

// runCDPCommands opens a one-shot CDP WebSocket to upstream, discovers the
// active page target, attaches with flatten=true, then runs the supplied
// commands sequentially against that session. Returns on the first error or
// after all commands succeed. The connection is closed before returning.
func runCDPCommands(ctx context.Context, upstream string, logger *slog.Logger, commands []cdpCommand) error {
	conn, _, err := websocket.Dial(ctx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return fmt.Errorf("dial upstream: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(10 * 1024 * 1024)

	var msgID int
	var mu sync.Mutex

	send := func(msg cdpMsg) error {
		b, _ := json.Marshal(msg)
		mu.Lock()
		defer mu.Unlock()
		return conn.Write(ctx, websocket.MessageText, b)
	}
	recv := func() (*cdpMsg, error) {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var m cdpMsg
		return &m, json.Unmarshal(b, &m)
	}
	nextID := func() int { msgID++; return msgID }

	// 1) Find the page target.
	id := nextID()
	if err := send(cdpMsg{ID: id, Method: "Target.getTargets", Params: json.RawMessage(`{}`)}); err != nil {
		return fmt.Errorf("getTargets send: %w", err)
	}
	var targetID string
	for {
		m, err := recv()
		if err != nil {
			return fmt.Errorf("getTargets recv: %w", err)
		}
		if m.ID != id {
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
			return fmt.Errorf("getTargets parse: %w", err)
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
		return fmt.Errorf("no page target found")
	}

	// 2) Attach to that page (flatten=true => commands carry sessionId).
	id = nextID()
	attachParams, _ := json.Marshal(map[string]any{"targetId": targetID, "flatten": true})
	if err := send(cdpMsg{ID: id, Method: "Target.attachToTarget", Params: attachParams}); err != nil {
		return fmt.Errorf("attach send: %w", err)
	}
	var sessionID string
	for {
		m, err := recv()
		if err != nil {
			return fmt.Errorf("attach recv: %w", err)
		}
		if m.ID != id {
			continue
		}
		var res struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(m.Result, &res); err != nil {
			return fmt.Errorf("attach parse: %w", err)
		}
		sessionID = res.SessionID
		break
	}
	if sessionID == "" {
		return fmt.Errorf("attach: empty sessionId")
	}

	// 3) Run each requested command in order.
	for _, cmd := range commands {
		id := nextID()
		params, _ := json.Marshal(cmd.Params)
		if err := send(cdpMsg{ID: id, Method: cmd.Method, Params: params, SessionID: sessionID}); err != nil {
			return fmt.Errorf("%s send: %w", cmd.Method, err)
		}
		for {
			m, err := recv()
			if err != nil {
				return fmt.Errorf("%s recv: %w", cmd.Method, err)
			}
			if m.ID != id {
				continue
			}
			if len(m.Error) > 0 {
				return fmt.Errorf("%s returned error: %s", cmd.Method, string(m.Error))
			}
			break
		}
	}
	return nil
}
