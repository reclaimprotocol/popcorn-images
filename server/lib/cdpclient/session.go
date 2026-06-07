package cdpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Session is a Client attached (flattened) to a single page target. All
// Call/Evaluate go through the stored sessionID. It is the page-level handle
// used by higher-level browser automation built on top of the local Chromium.
type Session struct {
	client    *Client
	sessionID string
}

// Attach picks the first non-devtools page target and attaches to it with
// flatten:true, returning a Session bound to that target's CDP session id.
// Mirrors the de-facto handshake used throughout the devtoolsproxy package:
// Target.getTargets -> Target.attachToTarget.
func Attach(ctx context.Context, c *Client) (*Session, error) {
	raw, err := c.Call(ctx, "Target.getTargets", nil, "")
	if err != nil {
		return nil, fmt.Errorf("Target.getTargets: %w", err)
	}
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, fmt.Errorf("unmarshal targets: %w", err)
	}
	var pageID string
	for _, t := range targets.TargetInfos {
		if t.Type == "page" && !strings.HasPrefix(t.URL, "devtools://") {
			pageID = t.TargetID
			break
		}
	}
	if pageID == "" {
		return nil, fmt.Errorf("no page target found")
	}
	attachRaw, err := c.Call(ctx, "Target.attachToTarget", map[string]any{
		"targetId": pageID,
		"flatten":  true,
	}, "")
	if err != nil {
		return nil, fmt.Errorf("Target.attachToTarget: %w", err)
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attachRaw, &att); err != nil {
		return nil, fmt.Errorf("unmarshal attach: %w", err)
	}
	return &Session{client: c, sessionID: att.SessionID}, nil
}

// SessionID returns the CDP session id this Session is bound to.
func (s *Session) SessionID() string { return s.sessionID }

// Client returns the underlying browser-level client.
func (s *Session) Client() *Client { return s.client }

// Call issues a CDP command on this page session.
func (s *Session) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.client.Call(ctx, method, params, s.sessionID)
}

// RuntimeEnable enables the Runtime domain (required before some evaluates on a
// fresh session).
func (s *Session) RuntimeEnable(ctx context.Context) error {
	_, err := s.Call(ctx, "Runtime.enable", nil)
	return err
}

// Evaluate runs Runtime.evaluate with returnByValue and returns the raw
// .result.value. Returns an error if the script throws.
func (s *Session) Evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	raw, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails,omitempty"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal evaluate result: %w", err)
	}
	if len(r.ExceptionDetails) > 0 {
		return nil, fmt.Errorf("evaluate exception: %s", string(r.ExceptionDetails))
	}
	return r.Result.Value, nil
}

// EvaluateBool evaluates an expression and decodes a boolean result. A null /
// non-bool value decodes as false.
func (s *Session) EvaluateBool(ctx context.Context, expression string) (bool, error) {
	v, err := s.Evaluate(ctx, expression)
	if err != nil {
		return false, err
	}
	var b bool
	_ = json.Unmarshal(v, &b)
	return b, nil
}

// EvaluateString evaluates an expression and decodes a string result. A null /
// non-string value decodes as "".
func (s *Session) EvaluateString(ctx context.Context, expression string) (string, error) {
	v, err := s.Evaluate(ctx, expression)
	if err != nil {
		return "", err
	}
	var str string
	_ = json.Unmarshal(v, &str)
	return str, nil
}

// Navigate enables the Page domain and navigates to url. It returns an error if
// CDP reports a navigation errorText (callers may treat ERR_ABORTED as soft).
func (s *Session) Navigate(ctx context.Context, url string) error {
	if _, err := s.Call(ctx, "Page.enable", nil); err != nil {
		return fmt.Errorf("Page.enable: %w", err)
	}
	raw, err := s.Call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return fmt.Errorf("Page.navigate: %w", err)
	}
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(raw, &r); err == nil && r.ErrorText != "" {
		return fmt.Errorf("navigation error: %s", r.ErrorText)
	}
	return nil
}

// Reload reloads the current page.
func (s *Session) Reload(ctx context.Context) error {
	_, err := s.Call(ctx, "Page.reload", nil)
	return err
}

// SetViewport overrides the device metrics (viewport) for this page.
func (s *Session) SetViewport(ctx context.Context, width, height int) error {
	_, err := s.Call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": 1,
		"mobile":            false,
	})
	return err
}

// CaptureScreenshot returns a base64-encoded PNG of the current page viewport.
func (s *Session) CaptureScreenshot(ctx context.Context) (string, error) {
	raw, err := s.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return "", err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("unmarshal screenshot: %w", err)
	}
	return r.Data, nil
}

// DispatchMouseClick dispatches a left-button press+release at (x, y).
func (s *Session) DispatchMouseClick(ctx context.Context, x, y float64) error {
	for _, t := range []string{"mousePressed", "mouseReleased"} {
		if _, err := s.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":       t,
			"x":          x,
			"y":          y,
			"button":     "left",
			"clickCount": 1,
		}); err != nil {
			return err
		}
	}
	return nil
}

// InsertText inserts a string at the current focus as if typed (fires
// beforeinput/input). For per-keystroke timing, callers invoke this per rune.
func (s *Session) InsertText(ctx context.Context, text string) error {
	_, err := s.Call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// Detach detaches this CDP session from its target. The underlying client/
// connection is left open for the caller to Close.
func (s *Session) Detach(ctx context.Context) error {
	_, err := s.client.Call(ctx, "Target.detachFromTarget", map[string]any{
		"sessionId": s.sessionID,
	}, "")
	return err
}
