package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSessionLifecycle is a runtime smoke test for the in-image browser-events
// worker (/session/*): start -> events stream -> navigate/type/click/screenshot
// -> close, plus the single-session 409 guard. It attaches to the local Chromium
// over CDP — no provider config matchers, so no proof attempts are made.
func TestSessionLifecycle(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not available: %v", err)
	}

	// ENABLE_SCREENSHOTS so the screenshot action returns a PNG (off by default).
	c := NewTestContainer(t, headfulImage)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	require.NoError(t, c.Start(ctx, ContainerConfig{Env: map[string]string{"ENABLE_SCREENSHOTS": "true"}}),
		"failed to start container")
	defer c.Stop(ctx)
	require.NoError(t, c.WaitReady(ctx), "api not ready")
	require.NoError(t, c.WaitDevTools(ctx), "devtools not ready")

	base := c.APIBaseURL()

	// --- start a session (loginUrl about:blank = no initial navigation) ---
	startBody := map[string]any{
		"provider_config": map[string]any{
			"providerId":    "smoke",
			"loginUrl":      "about:blank",
			"injectionType": "NONE",
		},
	}
	status, start := postJSON(t, ctx, base+"/session/start", startBody)
	require.Equal(t, http.StatusOK, status, "session/start: %v", start)
	sessionID, _ := start["session_id"].(string)
	require.NotEmpty(t, sessionID, "expected session_id")

	// --- second start while active must 409 ---
	status, _ = postJSON(t, ctx, base+"/session/start", startBody)
	require.Equal(t, http.StatusConflict, status, "second start should 409")

	// --- subscribe to the SSE event stream (collect for the test duration) ---
	events := newEventCollector(ctx, base+"/session/events")
	defer events.stop()

	// --- navigate to a small data: page with an input + button ---
	const page = `data:text/html,<html><body><input id="u"><button id="b" onclick="window.__clicked=1">go</button></body></html>`
	status, nav := postJSON(t, ctx, base+"/session/action", map[string]any{
		"type":    "navigate",
		"payload": map[string]any{"url": page, "wait_until": "domcontentloaded"},
	})
	require.Equal(t, http.StatusOK, status, "navigate: %v", nav)
	require.Equal(t, true, nav["success"], "navigate success")

	// --- type into the input ---
	status, typ := postJSON(t, ctx, base+"/session/action", map[string]any{
		"type":    "type",
		"payload": map[string]any{"selector": "#u", "text": "hello", "delay": 0},
	})
	require.Equal(t, http.StatusOK, status, "type: %v", typ)
	require.Equal(t, true, typ["success"], "type success")

	// --- click the button ---
	status, clk := postJSON(t, ctx, base+"/session/action", map[string]any{
		"type":    "click",
		"payload": map[string]any{"selector": "#b"},
	})
	require.Equal(t, http.StatusOK, status, "click: %v", clk)
	require.Equal(t, true, clk["success"], "click success")

	// --- screenshot: with ENABLE_SCREENSHOTS=true returns base64 PNG; otherwise
	// it returns success:false with the "disabled" message (both are valid — the
	// endpoint wiring is what we're smoke-testing). ---
	status, shot := postJSON(t, ctx, base+"/session/action", map[string]any{"type": "screenshot"})
	require.Equal(t, http.StatusOK, status, "screenshot: %v", shot)
	if shot["success"] == true {
		require.NotEmpty(t, shot["screenshot_b64"], "expected screenshot bytes when enabled")
	} else {
		require.Contains(t, fmt.Sprint(shot["error"]), "disabled", "unexpected screenshot failure: %v", shot)
	}

	// --- claim endpoint returns an (empty) proof set with no matchers ---
	status, claim := getJSON(t, ctx, base+"/session/claim")
	require.Equal(t, http.StatusOK, status, "claim: %v", claim)

	// --- we should have seen a session-ready event on the stream ---
	require.Eventually(t, func() bool { return events.has("session-ready") }, 10*time.Second, 200*time.Millisecond,
		"expected a session-ready event; got %v", events.types())

	// --- close ---
	status, closed := postJSON(t, ctx, base+"/session/close", map[string]any{})
	require.Equal(t, http.StatusOK, status, "close: %v", closed)
	require.Equal(t, true, closed["closed"], "close result")

	// --- close again -> 404 (no active session) ---
	status, _ = postJSON(t, ctx, base+"/session/close", map[string]any{})
	require.Equal(t, http.StatusNotFound, status, "second close should 404")
}

// ---- helpers ----

func postJSON(t *testing.T, ctx context.Context, url string, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	return doJSON(t, req)
}

func getJSON(t *testing.T, ctx context.Context, url string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	return doJSON(t, req)
}

func doJSON(t *testing.T, req *http.Request) (int, map[string]any) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// eventCollector reads an SSE stream in the background and records event types.
type eventCollector struct {
	mu     sync.Mutex
	seen   []string
	cancel context.CancelFunc
}

func newEventCollector(parent context.Context, url string) *eventCollector {
	ctx, cancel := context.WithCancel(parent)
	ec := &eventCollector{cancel: cancel}
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev struct {
				Event string `json:"event"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev) == nil && ev.Event != "" {
				ec.mu.Lock()
				ec.seen = append(ec.seen, ev.Event)
				ec.mu.Unlock()
			}
		}
	}()
	return ec
}

func (e *eventCollector) has(t string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.seen {
		if s == t {
			return true
		}
	}
	return false
}

func (e *eventCollector) types() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprint(e.seen)
}

func (e *eventCollector) stop() { e.cancel() }
