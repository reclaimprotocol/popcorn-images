package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/onkernel/kernel-images/server/lib/logger"
)

// Action defaults (mirroring the portal worker's action-executors).
const (
	defaultWaitUntil      = "domcontentloaded"
	defaultClickTimeoutMS = 10000
	defaultTypeDelayMS    = 100
	navTimeoutMS          = 30000 // BROWSER_TIMEOUT_MS
)

// ActionRequest is one action command. Fields are read from the flattened
// payload at the endpoint layer.
type ActionRequest struct {
	Type string

	URL             string // navigate
	WaitUntil       string // navigate; default domcontentloaded
	Selector        string // click|type|submit
	Text            string // type
	WaitForSelector *bool  // click; default true
	TimeoutMS       *int   // click|type|submit; default defaultClickTimeoutMS
	DelayMS         *int   // type; default defaultTypeDelayMS
}

// ActionResult mirrors the portal worker's { success, title?, url?, ... }.
type ActionResult struct {
	Success       bool   `json:"success"`
	Title         string `json:"title,omitempty"`
	URL           string `json:"url,omitempty"`
	ScreenshotB64 string `json:"screenshot_b64,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Execute dispatches an action on the active session.
func (m *Manager) Execute(ctx context.Context, req ActionRequest) (*ActionResult, error) {
	sess := m.Get()
	if sess == nil {
		return nil, ErrNoSession
	}
	sess.touch()

	switch req.Type {
	case "navigate":
		return m.execNavigate(ctx, sess, req)
	case "click":
		return m.execClick(ctx, sess, req)
	case "type":
		return m.execType(ctx, sess, req)
	case "submit":
		return m.execSubmit(ctx, sess, req)
	case "screenshot":
		return m.execScreenshot(ctx, sess)
	default:
		return nil, fmt.Errorf("unsupported action type: %s", req.Type)
	}
}

func (m *Manager) execNavigate(ctx context.Context, sess *Session, req ActionRequest) (*ActionResult, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("navigate requires url")
	}
	waitUntil := req.WaitUntil
	if waitUntil == "" {
		waitUntil = defaultWaitUntil
	}
	navCtx, cancel := context.WithTimeout(ctx, navTimeoutMS*time.Millisecond)
	defer cancel()

	if err := sess.cdp.Navigate(navCtx, req.URL); err != nil {
		return nil, err
	}
	_ = waitReadyState(navCtx, sess, waitUntil, navTimeoutMS*time.Millisecond)

	title, _ := sess.cdp.EvaluateString(ctx, "document.title")
	cur, _ := sess.cdp.EvaluateString(ctx, "location.href")
	return &ActionResult{Success: true, Title: title, URL: cur}, nil
}

func (m *Manager) execClick(ctx context.Context, sess *Session, req ActionRequest) (*ActionResult, error) {
	if req.Selector == "" {
		return nil, fmt.Errorf("click requires selector")
	}
	timeout := msOrDefault(req.TimeoutMS, defaultClickTimeoutMS)
	waitForSelector := req.WaitForSelector == nil || *req.WaitForSelector

	if waitForSelector {
		if err := waitForSelectorAttached(ctx, sess, req.Selector, timeout); err != nil {
			return nil, err
		}
	}

	visible, _ := sess.cdp.EvaluateBool(ctx, jsVisible(req.Selector))
	if visible {
		if err := m.clickElement(ctx, sess, req.Selector); err != nil {
			return nil, err
		}
		return &ActionResult{Success: true}, nil
	}

	// Not visible in viewport — fall back to a JS click.
	logger.FromContext(ctx).Info("element not visible, using JS click", "selector", req.Selector)
	if _, err := sess.cdp.Evaluate(ctx, jsClick(req.Selector)); err != nil {
		return nil, err
	}
	return &ActionResult{Success: true}, nil
}

func (m *Manager) execType(ctx context.Context, sess *Session, req ActionRequest) (*ActionResult, error) {
	if req.Selector == "" {
		return nil, fmt.Errorf("type requires selector")
	}
	timeout := msOrDefault(req.TimeoutMS, defaultClickTimeoutMS)
	delay := msOrDefault(req.DelayMS, defaultTypeDelayMS)

	if err := waitForSelectorVisible(ctx, sess, req.Selector, timeout); err != nil {
		return nil, err
	}
	// Focus the element and clear its existing value.
	if _, err := sess.cdp.Evaluate(ctx, jsFocusAndClear(req.Selector)); err != nil {
		return nil, err
	}
	// Type each rune as inserted text with the configured inter-keystroke delay.
	for _, ch := range req.Text {
		if err := sess.cdp.InsertText(ctx, string(ch)); err != nil {
			return nil, err
		}
		if delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return &ActionResult{Success: true}, nil
}

func (m *Manager) execSubmit(ctx context.Context, sess *Session, req ActionRequest) (*ActionResult, error) {
	if req.Selector == "" {
		return nil, fmt.Errorf("submit requires selector")
	}
	timeout := msOrDefault(req.TimeoutMS, defaultClickTimeoutMS)
	if err := waitForSelectorVisible(ctx, sess, req.Selector, timeout); err != nil {
		return nil, err
	}
	if err := m.clickElement(ctx, sess, req.Selector); err != nil {
		return nil, err
	}
	return &ActionResult{Success: true}, nil
}

func (m *Manager) execScreenshot(ctx context.Context, sess *Session) (*ActionResult, error) {
	if os.Getenv("ENABLE_SCREENSHOTS") != "true" {
		return &ActionResult{Success: false, Error: "Screenshot service is disabled"}, nil
	}
	b64, err := sess.cdp.CaptureScreenshot(ctx)
	if err != nil {
		return nil, err
	}
	return &ActionResult{Success: true, ScreenshotB64: b64}, nil
}

// clickElement scrolls the element into view, computes its center, and
// dispatches a real mouse click there.
func (m *Manager) clickElement(ctx context.Context, sess *Session, selector string) error {
	raw, err := sess.cdp.Evaluate(ctx, jsCenterAfterScroll(selector))
	if err != nil {
		return err
	}
	var center *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.Unmarshal(raw, &center); err != nil || center == nil {
		return fmt.Errorf("element %q not found for click", selector)
	}
	return sess.cdp.DispatchMouseClick(ctx, center.X, center.Y)
}

func msOrDefault(v *int, def int) time.Duration {
	if v != nil && *v >= 0 {
		return time.Duration(*v) * time.Millisecond
	}
	return time.Duration(def) * time.Millisecond
}
