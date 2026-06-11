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

// InputRequest is the JSON body of POST /cdp/input. Carries either text to
// insert verbatim, or a named special key (Backspace/Enter/Tab) to dispatch.
// `Count` lets the client request N repeats in a single request — used by the
// IME logic to flush a run of backspaces without firing N HTTP calls.
//
// Mobile typing routes through this endpoint instead of a direct WebSocket to
// the filtered CDP proxy on :9222: the gateway-routed HTTP path is the only
// one reachable from the user's browser in deployed environments, and each
// request is independent so a flaky-network drop only loses the one keystroke
// instead of forcing a socket reconnect that drops everything in flight.
type InputRequest struct {
	Text  string `json:"text,omitempty"`
	Key   string `json:"key,omitempty"`
	Count int    `json:"count,omitempty"`
}

// InputHandler returns an http.Handler that forwards typed input or named
// special keys to the upstream chromium via the dispatcher's persistent CDP
// session. The session stays open across requests so each keystroke pays only
// the network RTT — no per-request CDP attach handshake.
func InputHandler(d *InputDispatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req InputRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Text == "" && req.Key == "" {
			http.Error(w, "text or key required", http.StatusBadRequest)
			return
		}

		commands, err := buildInputCommands(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := d.Dispatch(ctx, commands); err != nil {
			d.logger.Warn("[Input] dispatch failed", "err", err)
			http.Error(w, "input failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

// buildInputCommands translates an InputRequest into the underlying CDP
// command sequence. Text goes through Input.insertText (handles Unicode
// transparently); named keys go through Input.dispatchKeyEvent as a
// keyDown+char+keyUp triplet so chromium handlers that listen on any phase
// still fire.
func buildInputCommands(req InputRequest) ([]cdpCommand, error) {
	if req.Text != "" {
		return []cdpCommand{{
			Method: "Input.insertText",
			Params: map[string]any{"text": req.Text},
		}}, nil
	}

	type keyDef struct {
		Key     string
		Code    string
		KeyCode int
		Text    string
	}
	keys := map[string]keyDef{
		"Backspace": {Key: "Backspace", Code: "Backspace", KeyCode: 8, Text: "\b"},
		"Enter":     {Key: "Enter", Code: "Enter", KeyCode: 13, Text: "\r"},
		"Tab":       {Key: "Tab", Code: "Tab", KeyCode: 9, Text: "\t"},
	}
	def, ok := keys[req.Key]
	if !ok {
		return nil, fmt.Errorf("unsupported key %q", req.Key)
	}

	count := req.Count
	if count <= 0 {
		count = 1
	}
	// Hard cap so a malformed client can't pin the dispatcher writing
	// arbitrarily long sequences.
	if count > 64 {
		count = 64
	}

	commands := make([]cdpCommand, 0, count*3)
	for i := 0; i < count; i++ {
		commands = append(commands,
			cdpCommand{
				Method: "Input.dispatchKeyEvent",
				Params: map[string]any{
					"type": "keyDown", "key": def.Key, "code": def.Code,
					"windowsVirtualKeyCode": def.KeyCode, "nativeVirtualKeyCode": def.KeyCode,
					"text": def.Text,
				},
			},
			cdpCommand{
				Method: "Input.dispatchKeyEvent",
				Params: map[string]any{
					"type": "char", "key": def.Key, "code": def.Code,
					"windowsVirtualKeyCode": def.KeyCode, "nativeVirtualKeyCode": def.KeyCode,
					"text": def.Text,
				},
			},
			cdpCommand{
				Method: "Input.dispatchKeyEvent",
				Params: map[string]any{
					"type": "keyUp", "key": def.Key, "code": def.Code,
					"windowsVirtualKeyCode": def.KeyCode, "nativeVirtualKeyCode": def.KeyCode,
				},
			},
		)
	}
	return commands, nil
}

// ---------------------------------------------------------------------------
// InputDispatcher: persistent CDP session for mobile-keyboard input.
//
// Holds one upstream WebSocket open with a page session already attached.
// HTTP handlers call Dispatch to send Input.* commands and await the per-
// command response. On disconnect (chromium restart, network blip) the
// dispatcher reconnects in the background and fails any in-flight calls so
// the HTTP layer can return an error rather than hang.
// ---------------------------------------------------------------------------

type pendingResp struct {
	msg *cdpMsg
	err error
}

type InputDispatcher struct {
	mgr    *UpstreamManager
	logger *slog.Logger
	cancel context.CancelFunc

	mu        sync.Mutex
	conn      *websocket.Conn // nil when disconnected
	sessionID string
	nextID    int
	pending   map[int]chan pendingResp
	// Closed when the current session becomes usable; replaced on each
	// reconnect cycle so waiters can re-arm.
	readyCh chan struct{}

	// Serializes writes on conn (coder/websocket forbids concurrent writes).
	writeMu sync.Mutex
}

// NewInputDispatcher creates and starts an InputDispatcher. Call Stop() to
// shut it down.
func NewInputDispatcher(mgr *UpstreamManager, logger *slog.Logger) *InputDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	d := &InputDispatcher{
		mgr:     mgr,
		logger:  logger,
		cancel:  cancel,
		pending: make(map[int]chan pendingResp),
		readyCh: make(chan struct{}),
	}
	go d.run(ctx)
	return d
}

// Stop terminates the background session and unblocks any waiters.
func (d *InputDispatcher) Stop() {
	d.cancel()
}

// run is the supervisor loop: connect+attach+pump until error, then back off
// and reconnect. Pending callers from the dead session are failed before the
// next attempt so they don't hang against a stale channel.
func (d *InputDispatcher) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		d.session(ctx)
		d.failAllPending(fmt.Errorf("CDP session disconnected"))
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// session dials upstream, attaches to the page target, publishes the session
// for Dispatch callers, and then runs the read pump until the connection
// closes or errors.
func (d *InputDispatcher) session(ctx context.Context) {
	upstream := d.mgr.Current()
	if upstream == "" {
		d.logger.Warn("[InputDispatcher] upstream not ready")
		return
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	conn, _, err := websocket.Dial(connCtx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		d.logger.Warn("[InputDispatcher] dial failed", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(10 * 1024 * 1024)

	var setupID int
	nextSetupID := func() int { setupID++; return setupID }

	writeSetup := func(msg cdpMsg) error {
		b, _ := json.Marshal(msg)
		return conn.Write(connCtx, websocket.MessageText, b)
	}
	recvUntil := func(wantID int) (*cdpMsg, error) {
		for {
			_, b, err := conn.Read(connCtx)
			if err != nil {
				return nil, err
			}
			var m cdpMsg
			if err := json.Unmarshal(b, &m); err != nil {
				continue
			}
			if m.ID == wantID {
				return &m, nil
			}
		}
	}

	// 1) Find a real page target.
	getID := nextSetupID()
	if err := writeSetup(cdpMsg{ID: getID, Method: "Target.getTargets", Params: json.RawMessage(`{}`)}); err != nil {
		d.logger.Warn("[InputDispatcher] getTargets send", "err", err)
		return
	}
	m, err := recvUntil(getID)
	if err != nil {
		d.logger.Warn("[InputDispatcher] getTargets recv", "err", err)
		return
	}
	var tgtRes struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(m.Result, &tgtRes); err != nil {
		d.logger.Warn("[InputDispatcher] getTargets parse", "err", err)
		return
	}
	var targetID string
	for _, t := range tgtRes.TargetInfos {
		if t.Type == "page" && !strings.HasPrefix(t.URL, "devtools://") {
			targetID = t.TargetID
			break
		}
	}
	// Prefer the active popup so keystrokes (and the WS hit-test that runs on
	// this session) target an open popup window — otherwise typing into an
	// OAuth login field would be dispatched to the opener and never arrive.
	// Snapshot the generation first so a later popup open/close triggers a
	// reconnect via the watcher goroutine below.
	attachGen := d.mgr.InputTargetGen()
	if popup := d.mgr.ActivePopup(); popup != "" {
		targetID = popup
	}
	if targetID == "" {
		d.logger.Warn("[InputDispatcher] no page target")
		return
	}

	// 2) Attach with flatten=true so subsequent commands carry sessionId.
	attID := nextSetupID()
	attParams, _ := json.Marshal(map[string]any{"targetId": targetID, "flatten": true})
	if err := writeSetup(cdpMsg{ID: attID, Method: "Target.attachToTarget", Params: attParams}); err != nil {
		d.logger.Warn("[InputDispatcher] attach send", "err", err)
		return
	}
	m, err = recvUntil(attID)
	if err != nil {
		d.logger.Warn("[InputDispatcher] attach recv", "err", err)
		return
	}
	var attRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(m.Result, &attRes); err != nil || attRes.SessionID == "" {
		d.logger.Warn("[InputDispatcher] attach parse", "err", err)
		return
	}

	// 3) Publish the live session so Dispatch can use it.
	d.mu.Lock()
	d.conn = conn
	d.sessionID = attRes.SessionID
	d.nextID = setupID
	readyCh := d.readyCh
	d.mu.Unlock()
	close(readyCh)
	d.logger.Info("[InputDispatcher] connected", "session", attRes.SessionID, "target", targetID)

	// Reconnect when the input target changes (popup opened/closed): cancel the
	// connection so the read pump below returns and run() re-selects the target.
	go func() {
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-t.C:
				if d.mgr.InputTargetGen() != attachGen {
					d.logger.Info("[InputDispatcher] input target changed, reconnecting")
					connCancel()
					return
				}
			}
		}
	}()

	// 4) Read pump: route IDed responses to pending waiters, drop events.
	for {
		_, b, err := conn.Read(connCtx)
		if err != nil {
			d.mu.Lock()
			d.conn = nil
			d.sessionID = ""
			d.readyCh = make(chan struct{})
			d.mu.Unlock()
			return
		}
		var resp cdpMsg
		if err := json.Unmarshal(b, &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
			continue // event
		}
		d.mu.Lock()
		ch, ok := d.pending[resp.ID]
		if ok {
			delete(d.pending, resp.ID)
		}
		d.mu.Unlock()
		if ok {
			ch <- pendingResp{msg: &resp}
		}
	}
}

// failAllPending releases every blocked Dispatch caller with the given error.
// Called when the underlying session dies so HTTP handlers don't hang past
// the read-pump teardown.
func (d *InputDispatcher) failAllPending(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, ch := range d.pending {
		ch <- pendingResp{err: err}
		delete(d.pending, id)
	}
}

// Dispatch sends commands sequentially on the persistent session, awaiting
// each response before sending the next. Blocks up to 2s waiting for the
// session to become ready if it isn't yet.
func (d *InputDispatcher) Dispatch(ctx context.Context, commands []cdpCommand) error {
	if err := d.waitReady(ctx); err != nil {
		return err
	}
	for _, cmd := range commands {
		if err := d.runOne(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

// DispatchOne runs a single command and returns the response message.
// Caller is responsible for parsing the result. Used by hit-test queries
// over the WS (Runtime.evaluate) which actually need the return value.
func (d *InputDispatcher) DispatchOne(ctx context.Context, cmd cdpCommand) (*cdpMsg, error) {
	if err := d.waitReady(ctx); err != nil {
		return nil, err
	}
	return d.runOneWithResult(ctx, cmd)
}

func (d *InputDispatcher) runOneWithResult(ctx context.Context, cmd cdpCommand) (*cdpMsg, error) {
	d.mu.Lock()
	conn := d.conn
	if conn == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("CDP session not connected")
	}
	d.nextID++
	id := d.nextID
	sessionID := d.sessionID
	ch := make(chan pendingResp, 1)
	d.pending[id] = ch
	d.mu.Unlock()

	paramsJSON, _ := json.Marshal(cmd.Params)
	msgBytes, _ := json.Marshal(cdpMsg{
		ID: id, Method: cmd.Method, Params: paramsJSON, SessionID: sessionID,
	})

	d.writeMu.Lock()
	err := conn.Write(ctx, websocket.MessageText, msgBytes)
	d.writeMu.Unlock()
	if err != nil {
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		if len(res.msg.Error) > 0 {
			return nil, fmt.Errorf("%s error: %s", cmd.Method, string(res.msg.Error))
		}
		return res.msg, nil
	}
}

func (d *InputDispatcher) waitReady(ctx context.Context) error {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		d.mu.Lock()
		ready := d.conn != nil
		readyCh := d.readyCh
		d.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readyCh:
			// Session came up; loop to re-check (could have died again).
		case <-deadline.C:
			return fmt.Errorf("CDP session not ready")
		}
	}
}

func (d *InputDispatcher) runOne(ctx context.Context, cmd cdpCommand) error {
	d.mu.Lock()
	conn := d.conn
	if conn == nil {
		d.mu.Unlock()
		return fmt.Errorf("CDP session not connected")
	}
	d.nextID++
	id := d.nextID
	sessionID := d.sessionID
	ch := make(chan pendingResp, 1)
	d.pending[id] = ch
	d.mu.Unlock()

	paramsJSON, _ := json.Marshal(cmd.Params)
	msgBytes, _ := json.Marshal(cdpMsg{
		ID: id, Method: cmd.Method, Params: paramsJSON, SessionID: sessionID,
	})

	d.writeMu.Lock()
	err := conn.Write(ctx, websocket.MessageText, msgBytes)
	d.writeMu.Unlock()
	if err != nil {
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if len(res.msg.Error) > 0 {
			return fmt.Errorf("%s error: %s", cmd.Method, string(res.msg.Error))
		}
		return nil
	}
}
