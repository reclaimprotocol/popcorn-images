package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/onkernel/kernel-images/server/lib/logger"
)

// daemonCommandRequest sends a command (not code) to the playwright daemon via Unix socket.
type daemonCommandRequest struct {
	ID      string                 `json:"id"`
	Command string                 `json:"command"`
	Params  map[string]interface{} `json:"params,omitempty"`
}

func respondJSON(w http.ResponseWriter, status int, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"error":"failed to marshal response"}`)
		return
	}
	writeJSON(w, status, string(b))
}

func (s *ApiService) sendDaemonCommand(ctx context.Context, command string, params map[string]interface{}, timeout time.Duration) (*playwrightDaemonResponse, error) {
	conn, err := net.DialTimeout("unix", playwrightDaemonSocket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout + 5*time.Second)); err != nil {
		return nil, fmt.Errorf("failed to set deadline: %w", err)
	}

	reqID := uuid.New().String()
	req := daemonCommandRequest{
		ID:      reqID,
		Command: command,
		Params:  params,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := conn.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	reader := bufio.NewReader(conn)
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var resp playwrightDaemonResponse
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.ID != reqID {
		return nil, fmt.Errorf("response ID mismatch: expected %s, got %s", reqID, resp.ID)
	}

	return &resp, nil
}

// HandleSessionInit initializes a browser-events session with script injection and network capture.
func (s *ApiService) HandleSessionInit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.FromContext(ctx)

	s.playwrightMu.Lock()
	defer s.playwrightMu.Unlock()

	if err := s.ensurePlaywrightDaemon(ctx); err != nil {
		log.Error("failed to ensure playwright daemon", "error", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to start daemon: %v", err)})
		return
	}

	var params map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp, err := s.sendDaemonCommand(ctx, "init_session", params, 30*time.Second)
	if err != nil {
		log.Error("session init failed", "error", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("session init failed: %v", err)})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

// HandleSessionData returns captured network requests and events.
func (s *ApiService) HandleSessionData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.FromContext(ctx)

	s.playwrightMu.Lock()
	defer s.playwrightMu.Unlock()

	if err := s.ensurePlaywrightDaemon(ctx); err != nil {
		log.Error("failed to ensure playwright daemon", "error", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("daemon not available: %v", err)})
		return
	}

	resp, err := s.sendDaemonCommand(ctx, "get_captured_data", nil, 5*time.Second)
	if err != nil {
		log.Error("get captured data failed", "error", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to get data: %v", err)})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

// HandleSessionClose closes the active browser-events session.
func (s *ApiService) HandleSessionClose(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.FromContext(ctx)

	s.playwrightMu.Lock()
	defer s.playwrightMu.Unlock()

	if err := s.ensurePlaywrightDaemon(ctx); err != nil {
		log.Error("failed to ensure playwright daemon", "error", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("daemon not available: %v", err)})
		return
	}

	var params map[string]interface{}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&params)
	}

	resp, err := s.sendDaemonCommand(ctx, "close_session", params, 5*time.Second)
	if err != nil {
		log.Error("session close failed", "error", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("session close failed: %v", err)})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}
