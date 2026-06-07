package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/onkernel/kernel-images/server/cmd/api/api/browser"
	"github.com/onkernel/kernel-images/server/lib/logger"
)

// Browser-events HTTP endpoints. These are registered directly on the chi
// router in main.go (like the sibling /cdp/* and /reclaim/* endpoints) rather
// than through the OpenAPI strict interface, so they need no codegen round-trip.
// JSON shapes match plans/spec-foundation-phases-0-2.md §3 so they can be
// promoted to OpenAPI-first later (Phase 6) mechanically.

// ---- request/response DTOs (snake_case wire contract) ----

type sessionStartRequest struct {
	SessionID      string             `json:"session_id"`
	ProviderConfig *providerConfigDTO `json:"provider_config"`
	Parameters     map[string]string  `json:"parameters"`
}

type providerConfigDTO struct {
	ProviderID      string       `json:"providerId"`
	AppID           string       `json:"appId"`
	LoginURL        string       `json:"loginUrl"`
	UserAgent       string       `json:"userAgent"`
	Viewport        *viewportDTO `json:"viewport"`
	InjectionType   string       `json:"injectionType"`
	CustomInjection string       `json:"customInjection"`
	LogLevel        string       `json:"logLevel"`
	// RequestData decodes directly into the browser matcher types.
	RequestData []browser.RequestMatcher `json:"requestData"`
}

type viewportDTO struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type sessionStartResult struct {
	SessionID  string `json:"session_id"`
	WsEndpoint string `json:"ws_endpoint,omitempty"`
	CurrentURL string `json:"current_url,omitempty"`
	Title      string `json:"title,omitempty"`
}

// sessionActionRequest accepts both the nested {type, payload:{...}} form and a
// flat {type, ...fields} form (the portal worker read `action.payload || action`).
type sessionActionRequest struct {
	Type    string             `json:"type"`
	Payload *actionPayloadDTO  `json:"payload"`
	actionPayloadDTO
}

type actionPayloadDTO struct {
	URL             string `json:"url"`
	WaitUntil       string `json:"wait_until"`
	Selector        string `json:"selector"`
	Text            string `json:"text"`
	WaitForSelector *bool  `json:"wait_for_selector"`
	Timeout         *int   `json:"timeout"`
	Delay           *int   `json:"delay"`
}

type sessionCloseResult struct {
	Closed    bool   `json:"closed"`
	SessionID string `json:"session_id,omitempty"`
}

func (b sessionStartRequest) toConfig() *browser.SessionConfig {
	cfg := &browser.SessionConfig{
		SessionID:  b.SessionID,
		Parameters: b.Parameters,
	}
	if b.ProviderConfig != nil {
		pc := &browser.ProviderConfig{
			ProviderID:      b.ProviderConfig.ProviderID,
			AppID:           b.ProviderConfig.AppID,
			LoginURL:        b.ProviderConfig.LoginURL,
			UserAgent:       b.ProviderConfig.UserAgent,
			InjectionType:   b.ProviderConfig.InjectionType,
			CustomInjection: b.ProviderConfig.CustomInjection,
			LogLevel:        b.ProviderConfig.LogLevel,
			RequestData:     b.ProviderConfig.RequestData,
		}
		if v := b.ProviderConfig.Viewport; v != nil {
			pc.Viewport = &browser.Viewport{Width: v.Width, Height: v.Height}
		}
		cfg.ProviderConfig = pc
		cfg.ProviderID = b.ProviderConfig.ProviderID
		cfg.AppID = b.ProviderConfig.AppID
	}
	return cfg
}

// effective merges the nested payload (if present) over the flat fields.
func (r sessionActionRequest) effective() actionPayloadDTO {
	if r.Payload != nil {
		return *r.Payload
	}
	return r.actionPayloadDTO
}

// ---- handlers ----

// HandleSessionStart attaches to the local browser and starts a session.
func (s *ApiService) HandleSessionStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.FromContext(ctx)

	var body sessionStartRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.ProviderConfig == nil {
		respondJSONError(w, http.StatusBadRequest, "provider_config is required")
		return
	}

	sess, err := s.browser.Start(ctx, body.toConfig())
	if err != nil {
		if errors.Is(err, browser.ErrSessionExists) {
			respondJSONError(w, http.StatusConflict, err.Error())
			return
		}
		log.Error("session start failed", "err", err)
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Best-effort current url/title snapshot.
	title, _ := sess.CurrentTitle(ctx)
	cur, _ := sess.CurrentURL(ctx)
	respondJSON(w, http.StatusOK, sessionStartResult{
		SessionID:  sess.SessionID,
		WsEndpoint: sess.WsEndpoint,
		CurrentURL: cur,
		Title:      title,
	})
}

// HandleSessionAction executes a selector-based action in the active session.
func (s *ApiService) HandleSessionAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.FromContext(ctx)

	var body sessionActionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Type == "" {
		respondJSONError(w, http.StatusBadRequest, "type is required")
		return
	}

	p := body.effective()
	req := browser.ActionRequest{
		Type:            body.Type,
		URL:             p.URL,
		WaitUntil:       p.WaitUntil,
		Selector:        p.Selector,
		Text:            p.Text,
		WaitForSelector: p.WaitForSelector,
		TimeoutMS:       p.Timeout,
		DelayMS:         p.Delay,
	}

	result, err := s.browser.Execute(ctx, req)
	if err != nil {
		if errors.Is(err, browser.ErrNoSession) {
			respondJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Error("session action failed", "type", body.Type, "err", err)
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

// HandleSessionClose closes the active session.
func (s *ApiService) HandleSessionClose(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := ""
	if sess := s.browser.Get(); sess != nil {
		sessionID = sess.SessionID
	}
	closed, err := s.browser.Close(ctx)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !closed {
		respondJSONError(w, http.StatusNotFound, "no active session")
		return
	}
	respondJSON(w, http.StatusOK, sessionCloseResult{Closed: true, SessionID: sessionID})
}

// HandleSessionClaim returns the proofs accumulated for the active session.
func (s *ApiService) HandleSessionClaim(w http.ResponseWriter, r *http.Request) {
	sess := s.browser.Get()
	if sess == nil {
		respondJSONError(w, http.StatusNotFound, "no active session")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.SessionID,
		"proofs":     s.browser.Claims(),
	})
}

// HandleSessionEvents streams session lifecycle and network events over SSE. It
// subscribes to the manager's event bus (fed by network capture and lifecycle),
// emits a session-ready snapshot if a session is already active, and sends a
// periodic heartbeat to keep the connection warm.
func (s *ApiService) HandleSessionEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-SSE-Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	subID, events := s.browser.Subscribe()
	defer s.browser.Unsubscribe(subID)

	emit := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
	}

	if sess := s.browser.Get(); sess != nil {
		emit(map[string]any{
			"event":      "session-ready",
			"session_id": sess.SessionID,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		})
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			emit(ev)
		case <-ticker.C:
			emit(map[string]any{
				"event":     "heartbeat",
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			})
		}
	}
}

// ---- small JSON helpers (named to avoid colliding with process.go's writeJSON) ----

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func respondJSONError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"message": message})
}
