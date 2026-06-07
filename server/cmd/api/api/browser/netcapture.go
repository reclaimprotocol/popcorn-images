package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// maxBodySize caps captured response bodies (matches the portal worker).
	maxBodySize = 500000
	// netReadLimit allows large getResponseBody payloads (matches the proxy).
	netReadLimit = 100 * 1024 * 1024
	// bodyFetchTimeout bounds a single Network.getResponseBody call.
	bodyFetchTimeout = 10 * time.Second
)

// cdpMsg is the CDP wire envelope (events have Method, replies have ID).
type cdpMsg struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// pendingRequest is the in-flight state for one captured request, keyed by the
// CDP requestId. Touched only by the reader goroutine, so it needs no lock.
type pendingRequest struct {
	RequestID   string
	URL         string
	Method      string
	ReqHeaders  map[string]string
	Status      int
	StatusText  string
	RespHeaders map[string]string
	MimeType    string
	FetchBody   bool
	MatcherIdx  int // index into matchers, or -1 if not a proof candidate
}

// NetCapture maintains a dedicated CDP connection that subscribes to Network and
// Page events and republishes them on the event bus. It uses its own connection
// (separate from the action session) because the request/reply cdpclient.Client
// discards events.
type NetCapture struct {
	upstream  UpstreamCurrenter
	logger    *slog.Logger
	sessionID string // browser-events session id, for event labeling
	publish   func(StreamEvent)

	// Proof pipeline (all optional — nil/empty means capture-only).
	matchers []RequestMatcher
	prove    Prover
	onClaim  func(*ClaimResult)

	cancel context.CancelFunc
}

func newNetCapture(upstream UpstreamCurrenter, logger *slog.Logger, sessionID string, publish func(StreamEvent), matchers []RequestMatcher, prove Prover, onClaim func(*ClaimResult)) *NetCapture {
	return &NetCapture{
		upstream:  upstream,
		logger:    logger,
		sessionID: sessionID,
		publish:   publish,
		matchers:  matchers,
		prove:     prove,
		onClaim:   onClaim,
	}
}

// Start begins capture in the background. Call Stop to shut it down.
func (nc *NetCapture) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	nc.cancel = cancel
	go nc.run(ctx)
}

// Stop terminates capture and its connection.
func (nc *NetCapture) Stop() {
	if nc.cancel != nil {
		nc.cancel()
	}
}

func (nc *NetCapture) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		nc.sessionLoop(ctx)
		// Connection died (or upstream not ready). Back off and reconnect; on
		// Chromium restart the upstream URL changes and Current() picks it up.
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// sessionLoop owns one CDP connection: handshake, enable domains, then read
// events until the connection fails. All connection state is local, so a
// reconnect starts cleanly.
func (nc *NetCapture) sessionLoop(ctx context.Context) {
	upstream := nc.upstream.Current()
	if upstream == "" {
		return
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	conn, _, err := websocket.Dial(connCtx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		nc.logger.Warn("[netcapture] dial failed", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(netReadLimit)

	var (
		msgID    int
		writeMu  sync.Mutex
		pendMu   sync.Mutex
		pending  = map[int]chan *cdpMsg{}
	)

	send := func(m cdpMsg) error {
		b, _ := json.Marshal(m)
		writeMu.Lock()
		defer writeMu.Unlock()
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
	nextID := func() int { msgID++; return msgID }

	// call issues a command and waits for its reply (routed by the reader). It
	// must run off the reader goroutine to avoid deadlock.
	call := func(method string, params any, sessionID string) (json.RawMessage, error) {
		pendMu.Lock()
		msgID++
		id := msgID
		ch := make(chan *cdpMsg, 1)
		pending[id] = ch
		pendMu.Unlock()
		defer func() {
			pendMu.Lock()
			delete(pending, id)
			pendMu.Unlock()
		}()

		raw, _ := json.Marshal(params)
		if err := send(cdpMsg{ID: id, Method: method, Params: raw, SessionID: sessionID}); err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(connCtx, bodyFetchTimeout)
		defer cancel()
		select {
		case <-cctx.Done():
			return nil, cctx.Err()
		case m := <-ch:
			if len(m.Error) > 0 {
				return nil, fmt.Errorf("cdp error: %s", string(m.Error))
			}
			return m.Result, nil
		}
	}

	// --- handshake: find page target, attach (flatten) ---
	pageSessionID, err := nc.handshake(send, recv, nextID)
	if err != nil {
		nc.logger.Warn("[netcapture] handshake failed", "err", err)
		return
	}

	// Enable the domains we observe on the page session.
	for _, m := range []cdpMsg{
		{ID: nextID(), Method: "Network.enable", Params: json.RawMessage(`{"maxResourceBufferSize":104857600,"maxTotalBufferSize":209715200}`), SessionID: pageSessionID},
		{ID: nextID(), Method: "Page.enable", Params: json.RawMessage(`{}`), SessionID: pageSessionID},
		{ID: nextID(), Method: "Page.setLifecycleEventsEnabled", Params: json.RawMessage(`{"enabled":true}`), SessionID: pageSessionID},
	} {
		if err := send(m); err != nil {
			nc.logger.Warn("[netcapture] enable failed", "method", m.Method, "err", err)
			return
		}
	}

	nc.publish(newEvent(EvSessionReady, nc.sessionID, SessionReadyData{WsEndpoint: upstream}))
	nc.logger.Info("[netcapture] capturing", "session_id", nc.sessionID, "cdp_session", pageSessionID)

	// --- reader loop ---
	inflight := map[string]*pendingRequest{}
	var lastMainURL string

	for {
		m, err := recv()
		if err != nil {
			return // reconnect
		}
		// Command reply → route to the waiting call().
		if m.ID != 0 {
			pendMu.Lock()
			ch, ok := pending[m.ID]
			pendMu.Unlock()
			if ok {
				select {
				case ch <- m:
				default:
				}
			}
			continue
		}

		switch m.Method {
		case "Network.requestWillBeSent":
			nc.onRequestWillBeSent(m.Params, inflight)
		case "Network.responseReceived":
			nc.onResponseReceived(m.Params, inflight)
		case "Network.loadingFinished":
			nc.onLoadingFinished(m.Params, inflight, pageSessionID, call)
		case "Network.loadingFailed":
			var p struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				delete(inflight, p.RequestID)
			}
		case "Page.frameNavigated":
			var p struct {
				Frame struct {
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(m.Params, &p) == nil && p.Frame.ParentID == "" {
				lastMainURL = p.Frame.URL
			}
		case "Page.loadEventFired":
			nc.publish(newEvent(EvPageLoaded, nc.sessionID, PageLoadedData{URL: lastMainURL}))
		case "Inspector.targetCrashed":
			nc.publish(newEvent(EvSessionClosed, nc.sessionID, SessionClosedData{Reason: "crash"}))
			return
		}
	}
}

func (nc *NetCapture) handshake(send func(cdpMsg) error, recv func() (*cdpMsg, error), nextID func() int) (string, error) {
	id1 := nextID()
	if err := send(cdpMsg{ID: id1, Method: "Target.getTargets", Params: json.RawMessage(`{}`)}); err != nil {
		return "", err
	}
	var targetID string
	for {
		m, err := recv()
		if err != nil {
			return "", err
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
			return "", err
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
		return "", fmt.Errorf("no page target found")
	}

	id2 := nextID()
	attachParams, _ := json.Marshal(map[string]any{"targetId": targetID, "flatten": true})
	if err := send(cdpMsg{ID: id2, Method: "Target.attachToTarget", Params: attachParams}); err != nil {
		return "", err
	}
	for {
		m, err := recv()
		if err != nil {
			return "", err
		}
		if m.ID == id2 {
			var res struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Result, &res)
			if res.SessionID == "" {
				return "", fmt.Errorf("no sessionId returned")
			}
			return res.SessionID, nil
		}
	}
}

func (nc *NetCapture) onRequestWillBeSent(params json.RawMessage, inflight map[string]*pendingRequest) {
	var p struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &p) != nil || p.RequestID == "" {
		return
	}
	inflight[p.RequestID] = &pendingRequest{
		RequestID:  p.RequestID,
		URL:        p.Request.URL,
		Method:     p.Request.Method,
		ReqHeaders: p.Request.Headers,
		MatcherIdx: -1,
	}
	nc.publish(newEvent(EvNetworkRequest, nc.sessionID, NetworkRequestData{
		ID:     p.RequestID,
		URL:    p.Request.URL,
		Method: p.Request.Method,
	}))
}

func (nc *NetCapture) onResponseReceived(params json.RawMessage, inflight map[string]*pendingRequest) {
	var p struct {
		RequestID string `json:"requestId"`
		Response  struct {
			Status     int               `json:"status"`
			StatusText string            `json:"statusText"`
			Headers    map[string]string `json:"headers"`
			MimeType   string            `json:"mimeType"`
		} `json:"response"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	pr := inflight[p.RequestID]
	if pr == nil {
		return
	}
	pr.Status = p.Response.Status
	pr.StatusText = p.Response.StatusText
	pr.RespHeaders = p.Response.Headers
	pr.MimeType = p.Response.MimeType
	pr.FetchBody = isCapturableMime(p.Response.MimeType)

	// Mark proof candidates and force body capture for them (the body is
	// needed to assemble the proof, regardless of the mime gate).
	for i := range nc.matchers {
		if matchesURL(nc.matchers[i], pr.URL, nil) {
			pr.MatcherIdx = i
			pr.FetchBody = true
			break
		}
	}
}

func (nc *NetCapture) onLoadingFinished(params json.RawMessage, inflight map[string]*pendingRequest, pageSessionID string, call func(string, any, string) (json.RawMessage, error)) {
	var p struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	pr := inflight[p.RequestID]
	if pr == nil {
		return
	}
	delete(inflight, p.RequestID)

	data := NetworkResponseData{
		ID:              pr.RequestID,
		URL:             pr.URL,
		Method:          pr.Method,
		Status:          pr.Status,
		StatusText:      pr.StatusText,
		ResponseHeaders: pr.RespHeaders,
		MimeType:        pr.MimeType,
	}
	if !pr.FetchBody {
		nc.publish(newEvent(EvNetworkResponse, nc.sessionID, data))
		return
	}
	// Fetch the body off the reader goroutine, publish, then (if this is a
	// proof candidate) assemble and run the proof in-process.
	go func() {
		fullBody := ""
		raw, err := call("Network.getResponseBody", map[string]any{"requestId": pr.RequestID}, pageSessionID)
		if err == nil {
			var r struct {
				Body          string `json:"body"`
				Base64Encoded bool   `json:"base64Encoded"`
			}
			if json.Unmarshal(raw, &r) == nil {
				fullBody = r.Body
				if r.Base64Encoded {
					if dec, e := base64.StdEncoding.DecodeString(r.Body); e == nil {
						fullBody = string(dec)
					}
				}
			}
		}
		body := fullBody
		if len(body) > maxBodySize {
			body = body[:maxBodySize]
		}
		data.ResponseBody = body
		nc.publish(newEvent(EvNetworkResponse, nc.sessionID, data))

		if pr.MatcherIdx >= 0 && pr.MatcherIdx < len(nc.matchers) && nc.prove != nil {
			nc.runProof(nc.matchers[pr.MatcherIdx], pr, body)
		}
	}()
}

// runProof assembles provider_params_json for a matched request and runs the
// reclaim-tee proof, emitting a sanitized claim event and recording the result.
func (nc *NetCapture) runProof(m RequestMatcher, pr *pendingRequest, body string) {
	ppj, err := buildProviderParamsJSON(m, capturedForProof{
		URL:    pr.URL,
		Method: pr.Method,
		Cookie: headerLookup(pr.ReqHeaders, "cookie"),
		Body:   body,
	})
	if err != nil {
		nc.logger.Warn("[netcapture] skip proof", "url", pr.URL, "err", err)
		nc.publish(newEvent(EvClaim, nc.sessionID, ClaimEventData{Error: err.Error()}))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	result, err := nc.prove(ctx, ppj, nc.sessionID)
	if err != nil {
		nc.logger.Error("[netcapture] proof failed", "url", pr.URL, "err", err)
		res := &ClaimResult{Error: err.Error()}
		if nc.onClaim != nil {
			nc.onClaim(res)
		}
		nc.publish(newEvent(EvClaim, nc.sessionID, ClaimEventData{Error: err.Error()}))
		return
	}
	if nc.onClaim != nil {
		nc.onClaim(result)
	}
	nc.publish(newEvent(EvClaim, nc.sessionID, ClaimEventData{
		Identifier: result.Identifier,
		Provider:   result.Provider,
	}))
}

// isCapturableMime gates body fetching to text-like content (matches the portal
// worker): text/*, application/json, application/javascript.
func isCapturableMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(mime, "text/") ||
		strings.HasPrefix(mime, "application/json") ||
		strings.HasPrefix(mime, "application/javascript")
}
