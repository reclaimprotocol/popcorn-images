package browser

import (
	"sync"
	"time"
)

// StreamEventType is the discriminant for a session event on the SSE stream.
type StreamEventType string

const (
	EvSessionReady    StreamEventType = "session-ready"
	EvPageLoaded      StreamEventType = "page-loaded"
	EvNetworkRequest  StreamEventType = "network-request"
	EvNetworkResponse StreamEventType = "network-response"
	EvClaim           StreamEventType = "claim"
	EvSessionClosed   StreamEventType = "session-closed"
	EvHeartbeat       StreamEventType = "heartbeat"
)

// StreamEvent is one event delivered to /session/events subscribers.
type StreamEvent struct {
	Event     StreamEventType `json:"event"`
	SessionID string          `json:"session_id,omitempty"`
	Timestamp string          `json:"timestamp"`
	Data      any             `json:"data,omitempty"`
}

// SessionReadyData is the payload for session-ready.
type SessionReadyData struct {
	WsEndpoint string `json:"ws_endpoint,omitempty"`
	CurrentURL string `json:"current_url,omitempty"`
	Title      string `json:"title,omitempty"`
}

// PageLoadedData is the payload for page-loaded.
type PageLoadedData struct {
	URL string `json:"url,omitempty"`
}

// NetworkRequestData is the payload for network-request.
type NetworkRequestData struct {
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}

// NetworkResponseData is the payload for network-response. ResponseBody is the
// public, truncated body; secrets/cookies are never placed here.
type NetworkResponseData struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Status          int               `json:"status"`
	StatusText      string            `json:"status_text,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
	MimeType        string            `json:"mime_type,omitempty"`
}

// SessionClosedData is the payload for session-closed.
type SessionClosedData struct {
	Reason string `json:"reason"` // "client" | "crash"
}

// ClaimEventData is the payload for a claim event (sanitized).
type ClaimEventData struct {
	Identifier string `json:"identifier,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Error      string `json:"error,omitempty"`
}

func newEvent(t StreamEventType, sessionID string, data any) StreamEvent {
	return StreamEvent{
		Event:     t,
		SessionID: sessionID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      data,
	}
}

// eventBus is a tiny fan-out broadcaster. Publishes are non-blocking: a slow
// subscriber drops events rather than stalling capture.
type eventBus struct {
	mu     sync.Mutex
	subs   map[int]chan StreamEvent
	nextID int
}

func newEventBus() *eventBus {
	return &eventBus{subs: make(map[int]chan StreamEvent)}
}

func (b *eventBus) subscribe() (int, <-chan StreamEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan StreamEvent, 256)
	b.subs[id] = ch
	return id, ch
}

func (b *eventBus) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

func (b *eventBus) publish(ev StreamEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // subscriber is slow; drop to avoid blocking capture
		}
	}
}
