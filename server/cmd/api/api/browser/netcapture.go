package browser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	PostData    string
	Status      int
	StatusText  string
	RespHeaders map[string]string
	MimeType    string
	FetchBody   bool
	// ProofCandidate is set when the URL+method match a matcher, so the body is
	// fetched and re-evaluated (URL+method+body) once available. URL match alone
	// never triggers a proof — the body gate decides.
	ProofCandidate bool
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

	// expectPublicData is set when the provider has a customInjection (which may
	// call updatePublicData) — proofs then wait briefly for it so every proof
	// carries publicData, not just the ones after it first appears.
	expectPublicData bool

	// report emits backend session-status transitions (session-bound; no-op when
	// no reporter is configured).
	report reportFunc

	// proxy, when non-nil, is the egress proxy for this session. The CDP loop
	// enables Fetch (handleAuthRequests) and answers the proxy 407 challenge
	// with these credentials — the username carries the sticky-session suffix so
	// the browser shares the TEE proof's exit IP. Set once before Start().
	proxy *ProxyConfig

	// proofMu guards proof dedup + outcome tracking. Dedup is per-REQUEST (a hash
	// of method+url+cookie+body), NOT per-matcher: a failed attempt on one
	// response (e.g. a logged-out page) must not block a later, different request
	// (e.g. the same API after login) from proving the same matcher. A matcher is
	// "done" only once it has a SUCCESSFUL proof; failures are never terminal.
	proofMu       sync.Mutex
	attemptedReq  map[string]bool // request hashes already attempted (identical request not retried)
	provenMatcher map[int]bool    // matchers with a successful proof
	inFlight      int             // proof attempts currently running (drives in_progress)

	// statusMu guards the one-shot status latches used to drive
	// LOGIN/USER_LOGGED_IN/PROOF_GENERATION_STARTED reporting.
	statusMu           sync.Mutex
	loginReported      bool
	proofStartReported bool

	// meta holds page-derived state surfaced via status: the latest
	// window.Reclaim publicData payload, the login interaction indicator, and
	// the current main-frame URL.
	metaMu         sync.Mutex
	publicData     string
	loginIndicator string
	currentURL     string

	cancel context.CancelFunc
}

func (nc *NetCapture) getPublicData() string {
	nc.metaMu.Lock()
	defer nc.metaMu.Unlock()
	return nc.publicData
}

// LoginIndicator returns the latest login interaction indicator
// ("none"|"url"|"element"|"timeout"), or "" if not yet known.
func (nc *NetCapture) LoginIndicator() string {
	nc.metaMu.Lock()
	defer nc.metaMu.Unlock()
	return nc.loginIndicator
}

// CurrentURL returns the latest main-frame URL seen by capture.
func (nc *NetCapture) CurrentURL() string {
	nc.metaMu.Lock()
	defer nc.metaMu.Unlock()
	return nc.currentURL
}

// SucceededCount returns how many distinct matchers have a successful proof.
func (nc *NetCapture) SucceededCount() int {
	nc.proofMu.Lock()
	defer nc.proofMu.Unlock()
	return len(nc.provenMatcher)
}

// InFlight returns how many proof attempts are currently running.
func (nc *NetCapture) InFlight() int {
	nc.proofMu.Lock()
	defer nc.proofMu.Unlock()
	return nc.inFlight
}

// waitPublicData returns publicData, waiting up to timeout for it to appear when
// the provider is expected to set it (customInjection present).
func (nc *NetCapture) waitPublicData(ctx context.Context, timeout time.Duration) string {
	if pd := nc.getPublicData(); pd != "" || !nc.expectPublicData {
		return pd
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nc.getPublicData()
		case <-ticker.C:
			if pd := nc.getPublicData(); pd != "" {
				return pd
			}
			if time.Now().After(deadline) {
				return nc.getPublicData()
			}
		}
	}
}

// requestHash identifies a captured request for dedup. It folds in the cookie
// header and post body so the same endpoint before and after login (different
// cookies) hashes differently and is therefore allowed a fresh proof attempt.
func requestHash(pr *pendingRequest) string {
	cookie := headerLookup(pr.ReqHeaders, "cookie")
	sum := sha256.Sum256([]byte(pr.Method + "\x00" + pr.URL + "\x00" + cookie + "\x00" + pr.PostData))
	return hex.EncodeToString(sum[:])
}

// beginAttempt decides whether to run a proof for matcher idx on this request.
// It returns false when the matcher already succeeded, or this exact request was
// already attempted. On true it records the attempt and increments in-flight.
func (nc *NetCapture) beginAttempt(idx int, reqHash string) bool {
	nc.proofMu.Lock()
	defer nc.proofMu.Unlock()
	if nc.provenMatcher[idx] {
		return false // matcher already proved successfully
	}
	if nc.attemptedReq == nil {
		nc.attemptedReq = map[string]bool{}
	}
	if nc.attemptedReq[reqHash] {
		return false // identical request already attempted
	}
	nc.attemptedReq[reqHash] = true
	nc.inFlight++
	return true
}

// finishAttempt records a proof outcome. Success marks the matcher proved (and
// reports PROOF_GENERATION_SUCCESS once every matcher has succeeded). Failure
// leaves the matcher open so a later matching request can retry — failures are
// never terminal for the session.
func (nc *NetCapture) finishAttempt(idx int, success bool) {
	nc.proofMu.Lock()
	if nc.provenMatcher == nil {
		nc.provenMatcher = map[int]bool{}
	}
	if success {
		nc.provenMatcher[idx] = true
	}
	if nc.inFlight > 0 {
		nc.inFlight--
	}
	done := len(nc.provenMatcher)
	nc.proofMu.Unlock()

	if success && len(nc.matchers) > 0 && done >= len(nc.matchers) {
		nc.report(statusProofGenerationSuccess, map[string]any{"totalProofsGenerated": done})
	}
}

func newNetCapture(upstream UpstreamCurrenter, logger *slog.Logger, sessionID string, publish func(StreamEvent), matchers []RequestMatcher, prove Prover, onClaim func(*ClaimResult), expectPublicData bool, report reportFunc) *NetCapture {
	if report == nil {
		report = func(string, map[string]any) {}
	}
	return &NetCapture{
		upstream:         upstream,
		logger:           logger,
		sessionID:        sessionID,
		publish:          publish,
		matchers:         matchers,
		prove:            prove,
		onClaim:          onClaim,
		expectPublicData: expectPublicData,
		report:           report,
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
		msgID   int
		writeMu sync.Mutex
		pendMu  sync.Mutex
		pending = map[int]chan *cdpMsg{}
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
	// nextID guards msgID under pendMu: the reader loop now allocates ids (proxy
	// Fetch responses) concurrently with call() running on the pollPublicData
	// goroutine, so the increment must be serialized with call()'s.
	nextID := func() int { pendMu.Lock(); msgID++; id := msgID; pendMu.Unlock(); return id }

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

	// Egress proxy auth. When a proxy is configured, enable Fetch with
	// handleAuthRequests so the proxy 407 surfaces as Fetch.authRequired (handled
	// in the reader loop). patterns:["*"] is required for authRequired to fire;
	// the matching Fetch.requestPaused events are continued unmodified, so this
	// stays an auth-only interception (Network.* capture/getResponseBody still
	// works normally on the continued requests).
	if nc.proxy != nil {
		if err := send(cdpMsg{ID: nextID(), Method: "Fetch.enable",
			Params:    json.RawMessage(`{"handleAuthRequests":true,"patterns":[{"urlPattern":"*"}]}`),
			SessionID: pageSessionID}); err != nil {
			nc.logger.Warn("[netcapture] Fetch.enable failed; proxy auth will not be answered", "err", err)
		} else {
			nc.logger.Info("[netcapture] Fetch enabled for proxy auth", "proxy_host", nc.proxy.Host)
		}
	}

	nc.publish(newEvent(EvSessionReady, nc.sessionID, SessionReadyData{WsEndpoint: upstream}))
	nc.logger.Info("[netcapture] capturing", "session_id", nc.sessionID, "cdp_session", pageSessionID)

	// Background poller: drain window.Reclaim's outbox and track the latest
	// publicData payload (set by customInjection via updatePublicData).
	go nc.pollPublicData(connCtx, pageSessionID, call)

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
		case "Fetch.authRequired":
			// Proxy 407 (or a site auth challenge). For a Proxy-source challenge
			// answer with the session's sticky-session credentials; for any other
			// source fall through to Default so we don't hijack site auth. Sent
			// off-loop via send() (fire-and-forget; the browser doesn't reply with
			// a result we need to await).
			var p struct {
				RequestID     string `json:"requestId"`
				AuthChallenge struct {
					Source string `json:"source"`
				} `json:"authChallenge"`
			}
			if json.Unmarshal(m.Params, &p) != nil {
				continue
			}
			var resp map[string]any
			if nc.proxy != nil && p.AuthChallenge.Source == "Proxy" {
				resp = map[string]any{
					"response": "ProvideCredentials",
					"username": nc.proxy.Username,
					"password": nc.proxy.Password,
				}
			} else {
				resp = map[string]any{"response": "Default"}
			}
			raw, _ := json.Marshal(map[string]any{"requestId": p.RequestID, "authChallengeResponse": resp})
			_ = send(cdpMsg{ID: nextID(), Method: "Fetch.continueWithAuth", Params: raw, SessionID: pageSessionID})
		case "Fetch.requestPaused":
			// patterns:["*"] pauses every request; continue unmodified so traffic
			// flows and Network.* capture is unaffected. (We only enabled Fetch to
			// catch authRequired.)
			var p struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(m.Params, &p) == nil && p.RequestID != "" {
				raw, _ := json.Marshal(map[string]any{"requestId": p.RequestID})
				_ = send(cdpMsg{ID: nextID(), Method: "Fetch.continueRequest", Params: raw, SessionID: pageSessionID})
			}
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
				nc.metaMu.Lock()
				nc.currentURL = lastMainURL
				nc.metaMu.Unlock()
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
			URL      string            `json:"url"`
			Method   string            `json:"method"`
			Headers  map[string]string `json:"headers"`
			PostData string            `json:"postData"`
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
		PostData:   p.Request.PostData,
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

	// Flag proof candidates and force body capture for them (the body is needed
	// both to evaluate the response-body gate and to assemble the proof). The
	// final URL+method+body decision is made in onLoadingFinished once the body
	// is available.
	for i := range nc.matchers {
		if matchesURL(nc.matchers[i], pr.URL, nil) && matchesMethod(nc.matchers[i], pr.Method) {
			pr.ProofCandidate = true
			pr.FetchBody = true
			break
		}
	}
}

// matchProof returns the index of the first matcher fully satisfied by this
// request (URL + method + response body), or -1 if none. A URL/method match
// whose body does not satisfy the matcher returns -1 — the caller then leaves
// the matcher unproven and waits for a later, matching response (e.g. the same
// API after login), rather than failing.
func (nc *NetCapture) matchProof(pr *pendingRequest, body string) int {
	for i := range nc.matchers {
		m := nc.matchers[i]
		if matchesURL(m, pr.URL, nil) && matchesMethod(m, pr.Method) && responseBodyMatches(m, body) {
			return i
		}
	}
	return -1
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

		if pr.ProofCandidate && nc.prove != nil {
			idx := nc.matchProof(pr, body)
			if idx < 0 {
				// URL/method matched but the body didn't satisfy the matcher
				// (e.g. logged-out response). Not a failure — wait for a later
				// response that does match.
				nc.logger.Debug("[netcapture] response body did not match matcher — waiting",
					"url", pr.URL, "method", pr.Method)
			} else if nc.beginAttempt(idx, requestHash(pr)) {
				// Resolve the cookie for the attestor replay. CDP's
				// requestWillBeSent headers omit the cookie (esp. httpOnly
				// session cookies), so fall back to the browser cookie jar for
				// the request URL (mirrors the portal's getCookiesFromBrowser).
				cookie := headerLookup(pr.ReqHeaders, "cookie")
				if cookie == "" {
					cookie = nc.fetchCookieString(call, pageSessionID, pr.URL)
				}
				nc.runProof(idx, nc.matchers[idx], pr, body, cookie)
			}
		}
	}()
}

// fetchCookieString returns the cookies the browser would send for url, as a
// "name=value; ..." header string. Uses CDP Network.getCookies, which reads the
// cookie jar (including httpOnly cookies the page's JS can't see). Returns "" on
// any error.
func (nc *NetCapture) fetchCookieString(call func(string, any, string) (json.RawMessage, error), sessionID, url string) string {
	raw, err := call("Network.getCookies", map[string]any{"urls": []string{url}}, sessionID)
	if err != nil {
		return ""
	}
	var r struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	parts := make([]string, 0, len(r.Cookies))
	for _, c := range r.Cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

const maxProofAttempts = 3

// isRetryableProofError reports whether a failed proof looks transient
// (network/TEE/RPC/timeout/panic) and worth retrying, vs a permanent
// validation/config error (schema mismatch, "Invalid receipt", body not found).
func isRetryableProofError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, t := range []string{
		"timed out", "timeout", "deadline exceeded",
		"failed to send request", "rpc request failed", "panicked",
		"connection reset", "connection refused", "eof",
		"temporarily", "unavailable", "no such host", "i/o timeout",
	} {
		if strings.Contains(msg, t) {
			return true
		}
	}
	return false
}

// runProof runs the reclaim-tee proof for a matched request (the Prover extracts
// response variables + assembles params), retrying transient failures, then
// emitting a sanitized claim event and recording the result for matcher idx.
func (nc *NetCapture) runProof(idx int, m RequestMatcher, pr *pendingRequest, body, cookie string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// First proof-generating request implies the user has logged in and proof
	// generation is starting — report both, once.
	nc.statusMu.Lock()
	firstProof := !nc.proofStartReported
	nc.proofStartReported = true
	nc.statusMu.Unlock()
	if firstProof {
		nc.report(statusUserLoggedIn, nil)
		nc.report(statusProofGenerationStarted, nil)
	}

	captured := CapturedForProof{
		URL:         pr.URL,
		Method:      pr.Method,
		Cookie:      cookie,
		Body:        body,
		RequestBody: pr.PostData,
		Headers:     pr.ReqHeaders,
		PublicData:  nc.waitPublicData(ctx, 4*time.Second),
	}

	var result *ClaimResult
	var err error
retryLoop:
	for attempt := 1; attempt <= maxProofAttempts; attempt++ {
		result, err = nc.prove(ctx, m, captured, nc.sessionID)
		if err == nil || !isRetryableProofError(err) || attempt == maxProofAttempts {
			break
		}
		nc.logger.Warn("[netcapture] proof attempt failed (retrying)", "attempt", attempt, "url", pr.URL, "err", err)
		select {
		case <-ctx.Done():
			break retryLoop
		case <-time.After(time.Duration(attempt) * 2 * time.Second):
		}
	}
	if err != nil {
		// A failed attempt is NOT terminal: the same matcher can still prove on a
		// later request (e.g. the same API after login). Surface the error as an
		// event for visibility, but don't persist a failed claim into
		// /session/claim (which feeds proof submission) and don't fail the
		// session — leave the matcher open for retry.
		nc.logger.Warn("[netcapture] proof failed — leaving matcher open for retry",
			"url", pr.URL, "matcher", idx, "err", err)
		identifier := ""
		if result != nil {
			identifier = result.Identifier
		}
		nc.publish(newEvent(EvClaim, nc.sessionID, ClaimEventData{Identifier: identifier, Error: err.Error()}))
		nc.finishAttempt(idx, false)
		return
	}
	if nc.onClaim != nil {
		nc.onClaim(result)
	}
	provider := ""
	if result.ClaimData != nil {
		provider = result.ClaimData.Provider
	}
	nc.publish(newEvent(EvClaim, nc.sessionID, ClaimEventData{
		Identifier: result.Identifier,
		Provider:   provider,
	}))
	nc.finishAttempt(idx, true)
}

// pollPublicData periodically drains window.Reclaim's outbox and records the
// latest publicData payload. _drain() clears the outbox; customInjection
// re-pushes publicData on each page load, so the latest value is kept current.
func (nc *NetCapture) pollPublicData(ctx context.Context, sessionID string, call func(string, any, string) (json.RawMessage, error)) {
	const drainExpr = `(function(){try{if(window.Reclaim&&typeof window.Reclaim._drain==='function'){return JSON.stringify(window.Reclaim._drain());}}catch(e){}return '[]';})()`
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		raw, err := call("Runtime.evaluate", map[string]any{"expression": drainExpr, "returnByValue": true}, sessionID)
		if err != nil {
			continue
		}
		var rv struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &rv) != nil || rv.Result.Value == "" || rv.Result.Value == "[]" {
			continue
		}
		var events []struct {
			Event   string          `json:"event"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal([]byte(rv.Result.Value), &events) != nil {
			continue
		}
		for _, e := range events {
			switch e.Event {
			case "publicData":
				if len(e.Message) > 0 {
					nc.metaMu.Lock()
					nc.publicData = string(e.Message)
					nc.metaMu.Unlock()
				}
			case "maybeRequiresLoginInteraction":
				var li struct {
					Indicator string `json:"indicator"`
				}
				if json.Unmarshal(e.Message, &li) == nil && li.Indicator != "" {
					nc.metaMu.Lock()
					nc.loginIndicator = li.Indicator
					nc.metaMu.Unlock()
					// First time the page signals a login interaction is needed,
					// report LOGIN_INDICATORS_FOUND (once).
					if li.Indicator == "url" || li.Indicator == "element" {
						nc.statusMu.Lock()
						first := !nc.loginReported
						nc.loginReported = true
						nc.statusMu.Unlock()
						if first {
							nc.report(statusLoginIndicatorsFound, nil)
						}
					}
				}
			}
		}
	}
}

// isCapturableMime gates body fetching to text-like content (matches the portal
// worker): text/*, application/json, application/javascript.
func isCapturableMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(mime, "text/") ||
		strings.HasPrefix(mime, "application/json") ||
		strings.HasPrefix(mime, "application/javascript")
}
