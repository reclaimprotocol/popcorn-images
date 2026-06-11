package devtoolsproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/onkernel/kernel-images/server/lib/scaletozero"
	"github.com/onkernel/kernel-images/server/lib/wsproxy"
)

var devtoolsListeningRegexp = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// UpstreamManager tails the Chromium supervisord log and extracts the current DevTools
// websocket URL, updating it whenever Chromium restarts and emits a new line.
type UpstreamManager struct {
	logFilePath string
	logger      *slog.Logger

	currentURL atomic.Value // string

	// touchEmulated tracks whether mobile/touch emulation is currently active.
	// Set by EmulateDeviceHandler when /cdp/emulate-device is called; read by
	// /computer/scroll to choose a touch-drag swipe over wheel ticks without a
	// per-scroll CDP probe.
	touchEmulated atomic.Bool

	// emuMu guards emuCommands: the last device-emulation command set requested
	// via /cdp/emulate-device. CDP's Emulation overrides are session-scoped and
	// are cleared when the setting session detaches — so the one-shot handler's
	// effect is lost on navigation. The persistent FocusTracker session re-applies
	// these on every navigation (and on a version bump) so mobile layout survives
	// page loads, redirects, and reconnects. emuVersion bumps on each Set so the
	// tracker can cheaply detect "config changed" without diffing the slice.
	emuMu       sync.RWMutex
	emuCommands []cdpCommand
	emuVersion  atomic.Uint64

	// activePopup holds the CDP targetId of the most recently opened popup
	// window (window.open), or "" when none is open. The PopupWatcher sets it
	// when a popup attaches and clears it when the popup is destroyed. The
	// input-ws push reads it to tell the client whether to show the in-app
	// "close popup" button (popups render fullscreen with no browser chrome,
	// so the user has no native way to close them), and /cdp/close-popup reads
	// it to know which target to close.
	activePopup atomic.Value // string
	// inputTargetGen bumps whenever the popup (and thus the target the
	// keyboard/focus machinery should act on) changes. FocusTracker and
	// InputDispatcher watch it and re-attach to the active popup while one is
	// open, then back to the main page when it closes — without it, typing and
	// the soft keyboard would stay bound to the opener and never reach the popup.
	inputTargetGen atomic.Uint64

	// pendingDialog holds a JavaScript dialog (alert/confirm/prompt) awaiting a
	// user response, or nil. Surfaced to the client (which renders its own
	// overlay in the emulated viewport) because the native dialog draws at the
	// 1920×1080 OS-window level — outside the mobile crop — and is suppressed by
	// Page.enable. dialogSeq hands out monotonic ids so a stale response can't
	// dismiss a newer dialog.
	pendingDialog atomic.Pointer[DialogInfo]
	dialogSeq     atomic.Uint64

	startOnce  sync.Once
	stopOnce   sync.Once
	cancelTail context.CancelFunc

	subsMu sync.RWMutex
	subs   map[chan string]struct{}
}

func NewUpstreamManager(logFilePath string, logger *slog.Logger) *UpstreamManager {
	um := &UpstreamManager{logFilePath: logFilePath, logger: logger}
	um.currentURL.Store("")
	um.activePopup.Store("")
	return um
}

// Start begins background tailing and updating the upstream URL until ctx is done.
func (u *UpstreamManager) Start(ctx context.Context) {
	u.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		u.cancelTail = cancel
		go u.tailLoop(ctx)
	})
}

// Stop cancels the background tailer.
func (u *UpstreamManager) Stop() {
	u.stopOnce.Do(func() {
		if u.cancelTail != nil {
			u.cancelTail()
		}
	})
}

// WaitForInitial blocks until an initial upstream URL has been discovered or the timeout elapses.
func (u *UpstreamManager) WaitForInitial(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if url := u.Current(); url != "" {
			return url, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("devtools upstream not found within %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Current returns the current upstream websocket URL if known, or empty string.
func (u *UpstreamManager) Current() string {
	val, _ := u.currentURL.Load().(string)
	return val
}

// SetTouchEmulated records whether touch/mobile emulation is active. Called by
// EmulateDeviceHandler after applying the Emulation.* overrides.
func (u *UpstreamManager) SetTouchEmulated(on bool) { u.touchEmulated.Store(on) }

// TouchEmulated reports the last touch-emulation state recorded via
// SetTouchEmulated. False until the first /cdp/emulate-device call.
func (u *UpstreamManager) TouchEmulated() bool { return u.touchEmulated.Load() }

// SetEmulationCommands records the device-emulation command set to be kept
// applied on the page. Called by EmulateDeviceHandler. The persistent
// FocusTracker session re-applies these so the override survives navigations
// (a one-shot CDP session loses it on detach). Passing nil clears them.
func (u *UpstreamManager) SetEmulationCommands(cmds []cdpCommand) {
	u.emuMu.Lock()
	u.emuCommands = cmds
	u.emuMu.Unlock()
	u.emuVersion.Add(1)
}

// EmulationCommands returns the last recorded device-emulation command set
// (nil if none). The returned slice must not be mutated by the caller.
func (u *UpstreamManager) EmulationCommands() []cdpCommand {
	u.emuMu.RLock()
	defer u.emuMu.RUnlock()
	return u.emuCommands
}

// EmulationVersion returns a counter that increments on every
// SetEmulationCommands call, so a re-applier can detect config changes cheaply.
func (u *UpstreamManager) EmulationVersion() uint64 { return u.emuVersion.Load() }

// SetActivePopup records the targetId of the currently open popup window, or
// "" to clear it. Called by the PopupWatcher. Bumps inputTargetGen on a real
// change so the keyboard/focus watchers re-target.
func (u *UpstreamManager) SetActivePopup(targetID string) {
	if prev, _ := u.activePopup.Load().(string); prev == targetID {
		return
	}
	u.activePopup.Store(targetID)
	u.inputTargetGen.Add(1)
}

// ActivePopup returns the targetId of the currently open popup window, or ""
// if none is open.
func (u *UpstreamManager) ActivePopup() string {
	v, _ := u.activePopup.Load().(string)
	return v
}

// InputTargetGen returns a counter that bumps whenever the keyboard/focus
// target should change (a popup opened or closed). FocusTracker and
// InputDispatcher snapshot it on attach and reconnect when it differs.
func (u *UpstreamManager) InputTargetGen() uint64 { return u.inputTargetGen.Load() }

// DialogInfo describes a JavaScript dialog awaiting a user response. Pushed to
// the client, which renders its own overlay in the emulated viewport.
type DialogInfo struct {
	ID            uint64 `json:"id"`
	Kind          string `json:"kind"`                    // alert | confirm | prompt
	Message       string `json:"message"`
	DefaultPrompt string `json:"defaultPrompt,omitempty"` // prefilled text for prompt
}

// SetPendingDialog records a dialog awaiting a response, or nil to clear it.
func (u *UpstreamManager) SetPendingDialog(d *DialogInfo) { u.pendingDialog.Store(d) }

// PendingDialog returns the dialog awaiting a response, or nil.
func (u *UpstreamManager) PendingDialog() *DialogInfo { return u.pendingDialog.Load() }

// NextDialogID returns a monotonic id so a stale client response can't dismiss a
// newer dialog.
func (u *UpstreamManager) NextDialogID() uint64 { return u.dialogSeq.Add(1) }

func (u *UpstreamManager) setCurrent(url string) {
	prev := u.Current()
	if url != "" && url != prev {
		u.logger.Info("devtools upstream updated", slog.String("url", url))
		u.currentURL.Store(url)
		// Broadcast update to subscribers without blocking. If a subscriber's
		// channel buffer (size 1) is full, replace the buffered value with the
		// latest update to avoid dropping notifications entirely.
		u.subsMu.RLock()
		for ch := range u.subs {
			select {
			case ch <- url:
				// sent successfully
			default:
				// channel is full; drop one stale value if present and try again
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- url:
				default:
					// still full; give up to remain non-blocking
				}
			}
		}
		u.subsMu.RUnlock()
	}
}

// Subscribe returns a channel that receives new upstream URLs as they are discovered.
// The returned cancel function should be called to unsubscribe and release resources.
func (u *UpstreamManager) Subscribe() (<-chan string, func()) {
	// use channel size 1 to avoid setCurrent blocking/stalling on slow subscribers
	// also provides "latest-wins" semantics: only one notification can sit in the channel
	ch := make(chan string, 1)
	u.subsMu.Lock()
	if u.subs == nil {
		u.subs = make(map[chan string]struct{})
	}
	u.subs[ch] = struct{}{}
	u.subsMu.Unlock()
	cancel := func() {
		u.subsMu.Lock()
		if _, ok := u.subs[ch]; ok {
			delete(u.subs, ch)
			close(ch)
		}
		u.subsMu.Unlock()
	}
	return ch, cancel
}

func (u *UpstreamManager) tailLoop(ctx context.Context) {
	backoff := 250 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		// Run one tail session. If it exits, retry with a small backoff.
		u.runTailOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		// cap backoff to 2s
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

func (u *UpstreamManager) runTailOnce(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "tail", "-f", "-n", "+1", u.logFilePath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		u.logger.Error("failed to open tail stdout", slog.String("err", err.Error()))
		return
	}
	if err := cmd.Start(); err != nil {
		// Common when file does not exist yet; log at debug level
		if strings.Contains(err.Error(), "No such file or directory") {
			u.logger.Debug("supervisord log not found yet; will retry", slog.String("path", u.logFilePath))
		} else {
			u.logger.Error("failed to start tail", slog.String("err", err.Error()))
		}
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		if matches := devtoolsListeningRegexp.FindStringSubmatch(line); len(matches) == 2 {
			u.setCurrent(matches[1])
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		u.logger.Error("tail scanner error", slog.String("err", err.Error()))
	}
}

func dialUpstreamWithRetry(ctx context.Context, mgr *UpstreamManager, urlCh <-chan string, initialUpstreamURL string, dialOpts *websocket.DialOptions, logger *slog.Logger) (*websocket.Conn, string, error) {
	upstreamURL := normalizeUpstreamURL(initialUpstreamURL)
	if upstreamURL == "" {
		return nil, "", fmt.Errorf("upstream not ready")
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()

	for {
		upstreamConn, _, err := websocket.Dial(ctx, upstreamURL, dialOpts)
		if err == nil {
			return upstreamConn, upstreamURL, nil
		}

		logger.Warn("dial upstream failed, checking for newer URL",
			slog.String("err", err.Error()), slog.String("url", upstreamURL))

		latestURL := normalizeUpstreamURL(mgr.Current())
		if latestURL != "" && latestURL != upstreamURL {
			upstreamURL = latestURL
			continue
		}

		select {
		case newURL, ok := <-urlCh:
			if !ok {
				return nil, "", fmt.Errorf("upstream unavailable")
			}
			newURL = normalizeUpstreamURL(newURL)
			if newURL == "" || newURL == upstreamURL {
				continue
			}
			upstreamURL = newURL
		case <-deadline.C:
			return nil, "", fmt.Errorf("timed out waiting for new upstream URL")
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
}

func maybePauseAfterCurrentRead(ctx context.Context, logger *slog.Logger, r *http.Request) {
	if r.URL.Query().Get("devtoolsProxyTestHook") != "1" {
		return
	}

	// Test-only hook used by e2e to widen the window between reading Current
	// and dialing/subscribing so reconnect races can be reproduced reliably.
	rawDelayMs := os.Getenv("DEVTOOLS_PROXY_TEST_POST_CURRENT_DELAY_MS")
	if rawDelayMs != "" {
		delayMs, err := strconv.Atoi(rawDelayMs)
		if err != nil || delayMs <= 0 {
			logger.Warn("ignoring invalid devtools proxy test delay", slog.String("value", rawDelayMs))
		} else {
			timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
			defer timer.Stop()

			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		}
	}

	blockPath := os.Getenv("DEVTOOLS_PROXY_TEST_POST_CURRENT_BLOCK_FILE")
	if blockPath == "" {
		return
	}

	readyPath := blockPath + ".ready"
	releasePath := blockPath + ".release"
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o644); err != nil {
		logger.Warn("failed to write devtools proxy test ready marker",
			slog.String("path", readyPath),
			slog.String("err", err.Error()))
		return
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(releasePath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			logger.Warn("failed to read devtools proxy test release marker",
				slog.String("path", releasePath),
				slog.String("err", err.Error()))
			return
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// WebSocketProxyHandler returns an http.Handler that upgrades incoming connections and
// proxies them to the current upstream websocket URL. It expects only websocket requests.
// If logCDPMessages is true, all CDP messages will be logged with their direction.
func WebSocketProxyHandler(mgr *UpstreamManager, logger *slog.Logger, logCDPMessages bool, ctrl scaletozero.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var transform wsproxy.MessageTransform
		if logCDPMessages {
			transform = func(direction string, mt websocket.MessageType, msg []byte) []byte {
				logCDPMessage(logger, direction, mt, msg)
				return msg
			}
		}

		acceptOpts := &websocket.AcceptOptions{
			OriginPatterns:  []string{"*"},
			CompressionMode: websocket.CompressionContextTakeover,
		}
		dialOpts := &websocket.DialOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		}

		// Subscribe to upstream URL changes so we can tear down stale sessions
		// when Chromium restarts and retry if the current URL is already dead.
		urlCh, unsub := mgr.Subscribe()
		defer unsub()

		upstreamCurrent := mgr.Current()
		if upstreamCurrent == "" {
			http.Error(w, "upstream not ready", http.StatusServiceUnavailable)
			return
		}

		maybePauseAfterCurrentRead(r.Context(), logger, r)

		// Accept the client WebSocket connection.
		clientConn, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			logger.Error("websocket accept failed", slog.String("err", err.Error()))
			return
		}
		clientConn.SetReadLimit(100 * 1024 * 1024)

		// Dial upstream. If the URL is stale (Chromium just restarted), first
		// re-check the manager's latest URL in case we missed the notification,
		// then wait briefly for the next update from Subscribe.
		upstreamConn, upstreamURL, err := dialUpstreamWithRetry(r.Context(), mgr, urlCh, upstreamCurrent, dialOpts, logger)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(r.Context().Err(), context.Canceled), errors.Is(r.Context().Err(), context.DeadlineExceeded):
				clientConn.Close(websocket.StatusGoingAway, "request cancelled")
			default:
				logger.Error("failed to connect to upstream", slog.String("err", err.Error()))
				clientConn.Close(websocket.StatusInternalError, "upstream unavailable")
			}
			return
		}
		upstreamConn.SetReadLimit(100 * 1024 * 1024)

		logger.Debug("proxying websocket", slog.String("url", upstreamURL))

		// Cancel the pump when the upstream URL changes (Chromium restarted),
		// forcing the client to reconnect with the new upstream.
		pumpCtx, pumpCancel := context.WithCancel(r.Context())

		go func(currentUpstreamURL string) {
			for {
				select {
				case newURL, ok := <-urlCh:
					if !ok {
						return
					}
					newURL = normalizeUpstreamURL(newURL)
					if newURL == "" || newURL == currentUpstreamURL {
						continue
					}
					logger.Info("upstream URL changed, closing stale proxy session",
						slog.String("old_url", currentUpstreamURL),
						slog.String("new_url", newURL))
					pumpCancel()
					return
				case <-pumpCtx.Done():
					return
				}
			}
		}(upstreamURL)

		var once sync.Once
		cleanup := func() {
			once.Do(func() {
				pumpCancel()
				upstreamConn.Close(websocket.StatusNormalClosure, "")
				clientConn.Close(websocket.StatusNormalClosure, "")
			})
		}

		wsproxy.Pump(pumpCtx, clientConn, upstreamConn, cleanup, logger, transform)
	})
}

// normalizeUpstreamURL parses a raw DevTools URL and returns a clean form.
func normalizeUpstreamURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path, RawQuery: parsed.RawQuery}).String()
}

// WebSocketProxyHandlerFiltered returns a filtered CDP proxy handler that only allows whitelisted commands
func WebSocketProxyHandlerFiltered(mgr *UpstreamManager, logger *slog.Logger, ctrl scaletozero.Controller) http.Handler {
	allowedCommands := createAllowedCommandsMap()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptOpts := &websocket.AcceptOptions{
			OriginPatterns:  []string{"*"},
			CompressionMode: websocket.CompressionContextTakeover,
		}
		dialOpts := &websocket.DialOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		}

		urlCh, unsub := mgr.Subscribe()
		defer unsub()

		upstreamCurrent := mgr.Current()
		if upstreamCurrent == "" {
			http.Error(w, "upstream not ready", http.StatusServiceUnavailable)
			return
		}

		maybePauseAfterCurrentRead(r.Context(), logger, r)

		clientConn, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			logger.Error("websocket accept failed", slog.String("err", err.Error()))
			return
		}
		clientConn.SetReadLimit(100 * 1024 * 1024)

		upstreamConn, upstreamURL, err := dialUpstreamWithRetry(r.Context(), mgr, urlCh, upstreamCurrent, dialOpts, logger)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(r.Context().Err(), context.Canceled), errors.Is(r.Context().Err(), context.DeadlineExceeded):
				clientConn.Close(websocket.StatusGoingAway, "request cancelled")
			default:
				logger.Error("failed to connect to upstream", slog.String("err", err.Error()))
				clientConn.Close(websocket.StatusInternalError, "upstream unavailable")
			}
			return
		}
		upstreamConn.SetReadLimit(100 * 1024 * 1024)

		logger.Debug("proxying websocket with CDP filtering", slog.String("url", upstreamURL))

		pumpCtx, pumpCancel := context.WithCancel(r.Context())

		go func(currentUpstreamURL string) {
			for {
				select {
				case newURL, ok := <-urlCh:
					if !ok {
						return
					}
					newURL = normalizeUpstreamURL(newURL)
					if newURL == "" || newURL == currentUpstreamURL {
						continue
					}
					logger.Info("upstream URL changed, closing stale proxy session",
						slog.String("old_url", currentUpstreamURL),
						slog.String("new_url", newURL))
					pumpCancel()
					return
				case <-pumpCtx.Done():
					return
				}
			}
		}(upstreamURL)

		var once sync.Once
		cleanup := func() {
			once.Do(func() {
				pumpCancel()
				upstreamConn.Close(websocket.StatusNormalClosure, "")
				clientConn.Close(websocket.StatusNormalClosure, "")
			})
		}

		// Use custom pump with CDP filtering
		pumpWithCDPFilter(pumpCtx, clientConn, upstreamConn, cleanup, logger, allowedCommands)
	})
}

// pumpWithCDPFilter bidirectionally copies messages between client and upstream
// with filtering on client->upstream direction for CDP commands
func pumpWithCDPFilter(ctx context.Context, client, upstream *websocket.Conn, onClose func(), logger *slog.Logger, allowedCommands map[string]bool) {
	errChan := make(chan error, 2)

	// Client -> Upstream (with filtering)
	go func() {
		for {
			mt, msg, err := client.Read(ctx)
			if err != nil {
				logger.Error("client read error", slog.String("err", err.Error()))
				errChan <- err
				return
			}

			// Filter CDP commands
			if mt == websocket.MessageText {
				var cdpMsg map[string]interface{}
				if err := json.Unmarshal(msg, &cdpMsg); err == nil {
					if method, ok := cdpMsg["method"].(string); ok && method != "" {
						if !allowedCommands[method] {
							logger.Warn("CDP command blocked by filter", slog.String("method", method))
							// Send error response back to client
							if id, hasID := cdpMsg["id"]; hasID {
								errResp := map[string]interface{}{
									"id":    id,
									"error": map[string]interface{}{"code": -32000, "message": "Command not allowed"},
								}
								if respBytes, err := json.Marshal(errResp); err == nil {
									_ = client.Write(ctx, websocket.MessageText, respBytes)
								}
							}
							continue
						}
					}
				}
			}

			if err := upstream.Write(ctx, mt, msg); err != nil {
				logger.Error("upstream write error", slog.String("err", err.Error()))
				errChan <- err
				return
			}
		}
	}()

	// Upstream -> Client (no filtering)
	go func() {
		for {
			mt, msg, err := upstream.Read(ctx)
			if err != nil {
				logger.Error("upstream read error", slog.String("err", err.Error()))
				errChan <- err
				return
			}

			if err := client.Write(ctx, mt, msg); err != nil {
				logger.Error("client write error", slog.String("err", err.Error()))
				errChan <- err
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-errChan:
	}
	onClose()
}

// createAllowedCommandsMap returns the whitelist of CDP commands allowed on the restricted endpoint
func createAllowedCommandsMap() map[string]bool {
	return map[string]bool{
		// Input - clipboard, scroll and input
		"Input.enable":            true, // Required to enable Input domain
		"Input.insertText":        true,
		"Input.dispatchKeyEvent":  true,
		"Input.dispatchMouseEvent": true,
		"Input.dispatchTouchEvent": true,

		// Emulation - viewport resize and visible area
		"Emulation.setDeviceMetricsOverride":   true,
		"Emulation.setVisibleSize":             true,
		"Emulation.setTouchEmulationEnabled":   true,
		"Emulation.clearDeviceMetricsOverride": true,

		// DOM - element detection at tap point
		"DOM.enable":             true, // Required to enable DOM domain
		"DOM.getNodeForLocation": true,
		"DOM.describeNode":       true,

		// Browser - CDP health ping
		"Browser.getVersion": true,

		// Target - popup handling, session management
		"Target.setAutoAttach":  true,
		"Target.attachToTarget": true, // Required to bootstrap page-scoped command sessions
		"Target.closeTarget":    true,
		"Target.getTargets":     true,

		// Page - navigation and lifecycle events
		"Page.enable": true, // Required to enable Page domain for events
		"Page.reload": true,
	}
}

// logCDPMessage logs a CDP message with its direction if logging is enabled
func logCDPMessage(logger *slog.Logger, direction string, mt websocket.MessageType, msg []byte) {
	if mt != websocket.MessageText {
		return // Only log text messages (CDP messages)
	}

	// Extract fields using regex from raw message
	rawMsg := string(msg)

	// Regex patterns to match "key":"val" or "key": "val" for string values
	extractStringField := func(key string) string {
		pattern := fmt.Sprintf(`"%s"\s*:\s*"([^"]*)"`, key)
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(rawMsg)
		if len(matches) > 1 {
			return matches[1]
		}
		return ""
	}

	// Regex pattern to match "key": number for numeric id
	extractNumberField := func(key string) interface{} {
		pattern := fmt.Sprintf(`"%s"\s*:\s*(\d+)`, key)
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(rawMsg)
		if len(matches) > 1 {
			// Try to parse as int first
			if val, err := strconv.Atoi(matches[1]); err == nil {
				return val
			}
			// Fall back to float64
			if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
				return val
			}
		}
		return nil
	}

	// Extract fields using regex
	method := extractStringField("method")
	id := extractNumberField("id")
	sessionId := extractStringField("sessionId")
	targetId := extractStringField("targetId")
	frameId := extractStringField("frameId")

	// Build log attributes, only including non-empty values
	attrs := []slog.Attr{
		slog.String("dir", direction),
	}

	if sessionId != "" {
		attrs = append(attrs, slog.String("sessionId", sessionId))
	}
	if targetId != "" {
		attrs = append(attrs, slog.String("targetId", targetId))
	}
	if id != nil {
		attrs = append(attrs, slog.Any("id", id))
	}
	if frameId != "" {
		attrs = append(attrs, slog.String("frameId", frameId))
	}

	if method != "" {
		attrs = append(attrs, slog.String("method", method))
	}

	attrs = append(attrs, slog.Int("raw_length", len(msg)))

	// Convert attrs to individual slog.Attr arguments
	args := make([]any, len(attrs))
	for i, attr := range attrs {
		args[i] = attr
	}

	logger.Info("cdp", args...)
}
