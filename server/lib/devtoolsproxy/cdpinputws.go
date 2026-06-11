package devtoolsproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// InputWSRequest is one frame on /cdp/input-ws. Same shape as InputRequest
// (the HTTP body) plus an optional Seq the client may echo back in acks so
// it can match acks to its outbox entries.
//
// Why a WebSocket as well as the HTTP endpoint:
//   - On cellular networks with multi-second RTT, the per-request TCP/TLS
//     setup + Cloudflare/proxy hops on each HTTP POST add easily 1–2 RTT of
//     overhead on top of the cellular leg. A persistent WS pays that setup
//     once, then each keystroke is a single frame on the open socket.
//   - Frames are ~20 bytes vs ~500 bytes for an HTTP request (headers).
//   - Ordering on a single WS is guaranteed by TCP — the client doesn't
//     need to single-flight on its end to preserve order.
//
// Reliability tradeoff vs HTTP: if the WS dies, every frame still in the
// kernel send buffer is lost, while independent HTTP POSTs would each retry.
// The client should treat the WS as best-effort and fall back to /cdp/input
// when the WS is closed.
type InputWSRequest struct {
	Seq   int64  `json:"seq,omitempty"`
	Text  string `json:"text,omitempty"`
	Key   string `json:"key,omitempty"`
	Count int    `json:"count,omitempty"`

	// HitTest is set when the client wants to know what element is under a
	// specific tap point (in remote viewport CSS pixels). Used by the
	// mobile-keyboard tap handler: relying on activeElement is unreliable
	// because clicks on non-focusable areas don't blur the previous input,
	// so isInput would stay true and the keyboard would re-pop on every
	// tap. elementFromPoint asks "what's actually under the pixel" — the
	// correct primitive for tap intent.
	HitTest *HitTestQuery `json:"hitTest,omitempty"`

	// Scroll is set for a pixel-precise mouse-wheel scroll. Dispatched as
	// Input.dispatchMouseEvent{mouseWheel} on the active target's session, so it
	// scrolls the element under (X,Y) smoothly (sub-notch, unlike the XTest wheel
	// opcode) and reaches popup windows. X/Y are layout-viewport CSS px; DeltaX/Y
	// are pixel scroll amounts (positive Y scrolls the page down).
	Scroll *ScrollFrame `json:"scroll,omitempty"`
}

type HitTestQuery struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type ScrollFrame struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	DeltaX float64 `json:"deltaX"`
	DeltaY float64 `json:"deltaY"`
}

// InputWSAck is the server-to-client confirmation frame. `Seq` echoes the
// client's seq so it can dequeue the matching outbox entry; `OK` is false
// when the dispatcher rejected the request (and `Err` carries why).
type InputWSAck struct {
	Type string `json:"type,omitempty"` // "ack" (omitempty kept for compat with older clients)
	Seq  int64  `json:"seq,omitempty"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
}

// InputWSFocus is a server-pushed snapshot of the FocusTracker cache.
// Sent on connect and whenever the focused element changes (by focusKey)
// so the client doesn't need to fire its own /cdp/active-element requests.
// On a high-RTT cellular link this collapses three concurrent fetches per
// tap (touchstart prefetch, touchend post-check, select bridge) into zero.
type InputWSFocus struct {
	Type string               `json:"type"` // always "focus"
	Info *ActiveElementResult `json:"info"`
}

// InputWSPopup tells the client whether a popup window is open. Pushed on
// connect and whenever the state flips. The client shows an in-app "close"
// button when Open is true — fullscreen popups have no browser chrome, so
// there is no native way for the user to close them.
type InputWSPopup struct {
	Type string `json:"type"` // always "popup"
	Open bool   `json:"open"`
}

// InputWSDialog tells the client a JavaScript dialog (alert/confirm/prompt) is
// awaiting a response, or clears it (Dialog nil). The client renders its own
// overlay in the emulated viewport since the native dialog can't be shown there.
type InputWSDialog struct {
	Type   string      `json:"type"` // always "dialog"
	Dialog *DialogInfo `json:"dialog"`
}

// InputWSHitTest is the server's reply to a HitTestQuery. `Seq` echoes the
// client's request seq. `IsInput` is true iff document.elementFromPoint at
// the queried coordinates resolved to a text-accepting input, textarea, or
// contenteditable. `FocusKey` and rect fields mirror ActiveElementResult so
// the client can apply the same readonly/disabled logic.
type InputWSHitTest struct {
	Type       string  `json:"type"` // always "hitTest"
	Seq        int64   `json:"seq"`
	IsInput    bool    `json:"isInput"`
	Tag        string  `json:"tag,omitempty"`
	Readonly   bool    `json:"readonly,omitempty"`
	Disabled   bool    `json:"disabled,omitempty"`
	IsEditable bool    `json:"isEditable,omitempty"`
	FocusKey   string  `json:"focusKey,omitempty"`
	Err        string  `json:"err,omitempty"`
}

// hitTestExpression: server-side JS to find the topmost element at (x, y)
// in the *layout viewport* (CSS pixels), descend into shadow roots and
// same-origin iframes, and report whether it's text-input-like.
const hitTestExpression = `
(function(x, y){
  function pierce(root, x, y) {
    var el = root.elementFromPoint(x, y);
    if (!el) return null;
    if (el.shadowRoot) {
      var deeper = pierce(el.shadowRoot, x, y);
      if (deeper) return deeper;
    }
    if (el.tagName && el.tagName.toLowerCase() === 'iframe' && el.contentDocument) {
      try {
        var rect = el.getBoundingClientRect();
        var deeper = pierce(el.contentDocument, x - rect.left, y - rect.top);
        if (deeper) return deeper;
      } catch (_) { /* cross-origin; treat the iframe itself as the hit */ }
    }
    return el;
  }
  // Walk up from the hit element looking for a focusable input ancestor —
  // taps often land on the inner <span> or icon of a wrapper input/contenteditable.
  function climb(el) {
    var n = el;
    while (n && n.nodeType === 1) {
      var tag = (n.tagName || '').toLowerCase();
      var type = (n.type || '').toLowerCase();
      var isTextType = type === '' || type === 'text' || type === 'email' || type === 'password' ||
                       type === 'search' || type === 'tel' || type === 'url' || type === 'number';
      if ((tag === 'input' && isTextType) || tag === 'textarea' || n.isContentEditable) {
        return n;
      }
      n = n.parentNode;
    }
    return null;
  }
  function focusKeyFor(node) {
    var parts = [];
    var n = node;
    while (n && n !== document.documentElement && parts.length < 12) {
      var idx = 0;
      var sib = n.parentNode ? n.parentNode.firstChild : null;
      while (sib && sib !== n) { idx++; sib = sib.nextSibling; }
      parts.push((n.nodeName || '?') + ':' + idx);
      n = n.parentNode;
    }
    var tagPart = (node.tagName || '').toLowerCase();
    var idPart = node.id ? '#' + node.id : '';
    return tagPart + idPart + '|' + parts.join('>');
  }
  var hit = pierce(document, x, y);
  if (!hit) return { isInput: false, tag: 'null' };
  var input = climb(hit);
  if (!input) {
    return { isInput: false, tag: (hit.tagName || '').toLowerCase() };
  }
  return {
    isInput: true,
    tag: (input.tagName || '').toLowerCase(),
    readonly: !!input.readOnly,
    disabled: !!input.disabled,
    isEditable: !!input.isContentEditable,
    focusKey: focusKeyFor(input)
  };
})(__X__, __Y__)
`

// InputWSHandler returns an http.Handler that upgrades to WebSocket and
// forwards each received frame to the persistent CDP InputDispatcher.
// Multiple clients can connect simultaneously — they all share the same
// dispatcher (and thus the same upstream chromium session), and the
// dispatcher serializes commands internally. When a FocusTracker is
// supplied, the connection also pushes active-element snapshots to the
// client whenever the focused element changes, so the client doesn't
// have to round-trip /cdp/active-element on every tap.
func InputWSHandler(d *InputDispatcher, ft *FocusTracker, mgr *UpstreamManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true, // same-origin enforced upstream via cloudflared/CORS
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			d.logger.Warn("[InputWS] accept failed", "err", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		conn.SetReadLimit(1 << 20) // 1 MiB; an InputRequest body is tiny

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		d.logger.Info("[InputWS] client connected", "remote", r.RemoteAddr)

		// writeMu serializes writes from the read loop (acks) and the focus
		// push goroutine — coder/websocket forbids concurrent writes.
		var writeMu syncMutex
		writeJSON := func(v any) {
			b, err := json.Marshal(v)
			if err != nil {
				return
			}
			writeCtx, wcancel := context.WithTimeout(ctx, 2*time.Second)
			defer wcancel()
			writeMu.Lock()
			defer writeMu.Unlock()
			_ = conn.Write(writeCtx, websocket.MessageText, b)
		}

		// Focus push: poll the FocusTracker cache and push snapshots on
		// change (or every ~2 s as a heartbeat). The poll interval here is
		// independent of the FocusTracker's own poll — we just read the
		// atomic pointer.
		if ft != nil || mgr != nil {
			go func() {
				ticker := time.NewTicker(200 * time.Millisecond)
				defer ticker.Stop()
				var lastFocusKey string
				var lastRectsHash uint64
				lastPopup := false
				lastDialogID := uint64(0)
				haveMgr := false
				heartbeatAt := time.Now()
				if ft != nil {
					if cur := ft.CurrentState(); cur != nil {
						writeJSON(InputWSFocus{Type: "focus", Info: cur})
						lastFocusKey = cur.FocusKey
						lastRectsHash = hashRects(cur.InputRects)
					}
				}
				if mgr != nil {
					haveMgr = true
					lastPopup = mgr.ActivePopup() != ""
					writeJSON(InputWSPopup{Type: "popup", Open: lastPopup})
					if d := mgr.PendingDialog(); d != nil {
						lastDialogID = d.ID
						writeJSON(InputWSDialog{Type: "dialog", Dialog: d})
					}
				}
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
					// Popup + dialog state: push immediately on a change so the
					// close button / dialog overlay appear without waiting on
					// focus churn.
					if haveMgr {
						if open := mgr.ActivePopup() != ""; open != lastPopup {
							lastPopup = open
							writeJSON(InputWSPopup{Type: "popup", Open: open})
						}
						d := mgr.PendingDialog()
						curID := uint64(0)
						if d != nil {
							curID = d.ID
						}
						if curID != lastDialogID {
							lastDialogID = curID
							writeJSON(InputWSDialog{Type: "dialog", Dialog: d})
						}
					}
					if ft == nil {
						continue
					}
					cur := ft.CurrentState()
					if cur == nil {
						continue
					}
					rectsHash := hashRects(cur.InputRects)
					// Push when focus changed, when input-rect layout
					// changed (new modal, scroll, route change), or on a
					// 600 ms heartbeat — the client's tap-time hit-test
					// relies on these rects being fresh.
					if cur.FocusKey != lastFocusKey || rectsHash != lastRectsHash ||
						time.Since(heartbeatAt) > 600*time.Millisecond {
						writeJSON(InputWSFocus{Type: "focus", Info: cur})
						lastFocusKey = cur.FocusKey
						lastRectsHash = rectsHash
						heartbeatAt = time.Now()
					}
				}
			}()
		}

		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				d.logger.Info("[InputWS] client disconnected", "err", err)
				return
			}

			var req InputWSRequest
			if err := json.Unmarshal(b, &req); err != nil {
				writeJSON(InputWSAck{Type: "ack", Seq: 0, OK: false, Err: "invalid json: " + err.Error()})
				continue
			}

			// Hit-test query — answer with elementFromPoint result.
			if req.HitTest != nil {
				go func(seq int64, q HitTestQuery) {
					htCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					expr := strings.NewReplacer(
						"__X__", strconv.FormatFloat(q.X, 'f', -1, 64),
						"__Y__", strconv.FormatFloat(q.Y, 'f', -1, 64),
					).Replace(hitTestExpression)
					msg, err := d.DispatchOne(htCtx, cdpCommand{
						Method: "Runtime.evaluate",
						Params: map[string]any{"expression": expr, "returnByValue": true},
					})
					if err != nil {
						writeJSON(InputWSHitTest{Type: "hitTest", Seq: seq, Err: err.Error()})
						return
					}
					// Parse Runtime.evaluate's nested .result.value
					var wrap struct {
						Result struct {
							Value map[string]any `json:"value"`
						} `json:"result"`
					}
					if err := json.Unmarshal(msg.Result, &wrap); err != nil {
						writeJSON(InputWSHitTest{Type: "hitTest", Seq: seq, Err: err.Error()})
						return
					}
					val := wrap.Result.Value
					isInput, _ := val["isInput"].(bool)
					tag, _ := val["tag"].(string)
					ro, _ := val["readonly"].(bool)
					dis, _ := val["disabled"].(bool)
					ed, _ := val["isEditable"].(bool)
					fk, _ := val["focusKey"].(string)
					writeJSON(InputWSHitTest{
						Type: "hitTest", Seq: seq, IsInput: isInput, Tag: tag,
						Readonly: ro, Disabled: dis, IsEditable: ed, FocusKey: fk,
					})
				}(req.Seq, *req.HitTest)
				continue
			}

			// Mouse-wheel scroll — pixel-precise, on the active target session
			// (reaches popups; scrolls the element under the cursor). Best-effort:
			// no ack (scroll is high-frequency and idempotent enough that the
			// client doesn't track per-frame delivery). Dispatched synchronously
			// to preserve order — the CDP upstream is local so this is sub-ms.
			if req.Scroll != nil {
				dispatchCtx, dcancel := context.WithTimeout(ctx, 3*time.Second)
				if err := d.Dispatch(dispatchCtx, []cdpCommand{{
					Method: "Input.dispatchMouseEvent",
					Params: map[string]any{
						"type":   "mouseWheel",
						"x":      req.Scroll.X,
						"y":      req.Scroll.Y,
						"deltaX": req.Scroll.DeltaX,
						"deltaY": req.Scroll.DeltaY,
					},
				}}); err != nil {
					d.logger.Warn("[InputWS] scroll dispatch failed", "err", err)
				}
				dcancel()
				continue
			}

			if req.Text == "" && req.Key == "" {
				writeJSON(InputWSAck{Type: "ack", Seq: req.Seq, OK: false, Err: "text or key required"})
				continue
			}

			commands, err := buildInputCommands(InputRequest{Text: req.Text, Key: req.Key, Count: req.Count})
			if err != nil {
				writeJSON(InputWSAck{Type: "ack", Seq: req.Seq, OK: false, Err: err.Error()})
				continue
			}

			// Per-frame timeout so a slow/stuck CDP doesn't block subsequent
			// frames on this socket — though Dispatch is serialized, a
			// hanging command would still queue follow-ups behind it.
			dispatchCtx, dcancel := context.WithTimeout(ctx, 5*time.Second)
			dispatchErr := d.Dispatch(dispatchCtx, commands)
			dcancel()
			if dispatchErr != nil {
				d.logger.Warn("[InputWS] dispatch failed", "err", dispatchErr)
				writeJSON(InputWSAck{Type: "ack", Seq: req.Seq, OK: false, Err: dispatchErr.Error()})
				continue
			}

			writeJSON(InputWSAck{Type: "ack", Seq: req.Seq, OK: true})
		}
	})
}

// syncMutex is a tiny local alias so we don't import sync just for one Mutex
// in this file — keeps the surface area visible.
type syncMutex = sync.Mutex

// hashRects produces a cheap stable hash of an input-rect set so the WS
// push loop can detect layout changes without diffing the slice element by
// element. Uses FNV-1a; collisions are tolerable because a missed push just
// delays the client cache by one 600 ms heartbeat.
func hashRects(rects []SelectRect) uint64 {
	const offset = 14695981039346656037
	const prime = 1099511628211
	h := uint64(offset)
	for _, r := range rects {
		// Quantize to integer pixels — sub-pixel jitter shouldn't trigger
		// a push, but real layout shifts (toolbar opens, route change) will
		// move rects by tens of pixels and definitely change the hash.
		ix := int64(r.X)
		iy := int64(r.Y)
		iw := int64(r.Width)
		ih := int64(r.Height)
		for _, v := range [4]int64{ix, iy, iw, ih} {
			b := uint64(v)
			h ^= b
			h *= prime
		}
	}
	return h
}
