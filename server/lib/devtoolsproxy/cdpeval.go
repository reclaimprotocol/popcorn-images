package devtoolsproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	// For responses
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
	// For session-scoped messages
	SessionID string `json:"sessionId,omitempty"`
}

// EvalActiveElement connects to Chromium's CDP, finds the active page, and
// evaluates document.activeElement to determine if it is an input field.
// It does NOT go through the WebSocket proxy – it connects directly to avoid
// compression and lifecycle issues that affect the browser-native WebSocket.
func EvalActiveElement(ctx context.Context, mgr *UpstreamManager) (*ActiveElementResult, error) {
	upstream := mgr.Current()
	if upstream == "" {
		return nil, fmt.Errorf("upstream not ready")
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, upstream, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("dial upstream: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(10 * 1024 * 1024)

	send := func(msg cdpMsg) error {
		b, _ := json.Marshal(msg)
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

	// Step 1: get all targets
	if err := send(cdpMsg{ID: 1, Method: "Target.getTargets", Params: json.RawMessage(`{}`)}); err != nil {
		return nil, fmt.Errorf("getTargets send: %w", err)
	}

	var targetID string
	for {
		m, err := recv()
		if err != nil {
			return nil, fmt.Errorf("getTargets recv: %w", err)
		}
		if m.ID != 1 {
			continue
		}
		// Parse targets
		var res struct {
			TargetInfos []struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				URL      string `json:"url"`
			} `json:"targetInfos"`
		}
		if err := json.Unmarshal(m.Result, &res); err != nil {
			return nil, fmt.Errorf("getTargets parse: %w", err)
		}
		// Find first page target (excluding devtools:// internal pages)
		for _, t := range res.TargetInfos {
			if t.Type == "page" && !strings.HasPrefix(t.URL, "devtools://") {
				targetID = t.TargetID
				break
			}
		}
		break
	}

	if targetID == "" {
		// No real page target (e.g., only chrome://newtab/) – no web input possible
		return &ActiveElementResult{IsInput: false, Tag: "no-page-target"}, nil
	}

	// Step 2: attach to the page target
	attachParams, _ := json.Marshal(map[string]interface{}{"targetId": targetID, "flatten": true})
	if err := send(cdpMsg{ID: 2, Method: "Target.attachToTarget", Params: attachParams}); err != nil {
		return nil, fmt.Errorf("attachToTarget send: %w", err)
	}

	var sessionID string
	for {
		m, err := recv()
		if err != nil {
			return nil, fmt.Errorf("attachToTarget recv: %w", err)
		}
		if m.ID == 2 {
			var res struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Result, &res)
			sessionID = res.SessionID
			break
		}
	}

	if sessionID == "" {
		return nil, fmt.Errorf("no sessionId returned from attachToTarget")
	}

	// Step 3: evaluate
	evalParams, _ := json.Marshal(map[string]interface{}{
		"expression":    activeElementExpression,
		"returnByValue": true,
	})
	if err := send(cdpMsg{ID: 3, Method: "Runtime.evaluate", Params: evalParams, SessionID: sessionID}); err != nil {
		return nil, fmt.Errorf("evaluate send: %w", err)
	}

	for {
		m, err := recv()
		if err != nil {
			return nil, fmt.Errorf("evaluate recv: %w", err)
		}
		if m.ID != 3 {
			continue
		}
		var res struct {
			Result struct {
				Value json.RawMessage `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(m.Result, &res); err != nil {
			return nil, fmt.Errorf("evaluate parse: %w", err)
		}
		var val map[string]interface{}
		if err := json.Unmarshal(res.Result.Value, &val); err != nil {
			return &ActiveElementResult{IsInput: false, Tag: "parse-error"}, nil
		}

		if errStr, ok := val["error"].(string); ok {
			return &ActiveElementResult{IsInput: false, Tag: errStr}, nil
		}
		if val["isCrossoriginIframe"] == true {
			return &ActiveElementResult{IsInput: true, Tag: "iframe"}, nil
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
		}, nil
	}
}

// ActiveElementHandler returns an http.Handler that evaluates the active element
// via CDP and responds with JSON. It must be registered on the rDevtools router
// so it already has CORS headers applied by the middleware.
func ActiveElementHandler(mgr *UpstreamManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := EvalActiveElement(r.Context(), mgr)
		if err != nil {
			// Optimistic default on error so keyboards stay up
			result = &ActiveElementResult{IsInput: true, Tag: "server-error"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
