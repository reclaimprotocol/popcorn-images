// Package reclaimbackend is a dependency-free client for the Reclaim backend
// (api.reclaimprotocol.org). It ports the parts of the portal's
// ReclaimSdkService + featureFlag.ts that the in-image worker needs:
// session bootstrap (getSession + getProvider), session-status updates,
// the usePortalTEE feature flag, and proof callback submission.
//
// Auth is via the X-Reclaim-Session-Id header only — the image never holds an
// app secret. It must not log request/response bodies (they can carry secrets).
package reclaimbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ReclaimSessionStatus values (mirrors shared/src/reclaim-sdk.service.ts).
const (
	StatusUserInitVerification    = "USER_INIT_VERIFICATION"
	StatusUserStartedVerification = "USER_STARTED_VERIFICATION"
	StatusLoginIndicatorsFound    = "LOGIN_INDICATORS_FOUND"
	StatusLoginIndicatorsNotFound = "LOGIN_INDICATORS_NOT_FOUND"
	StatusUserLoggedIn            = "USER_LOGGED_IN"
	StatusProofGenerationStarted  = "PROOF_GENERATION_STARTED"
	StatusProofGenerationRetry    = "PROOF_GENERATION_RETRY"
	StatusProofGenerationFailed   = "PROOF_GENERATION_FAILED"
	StatusProofGenerationSuccess  = "PROOF_GENERATION_SUCCESS"
	StatusProofSubmissionFailed   = "PROOF_SUBMISSION_FAILED"
	StatusProofSubmitted          = "PROOF_SUBMITTED"
	StatusReclaimException        = "RECLAIM_EXCEPTION"
	StatusSessionCancelled        = "SESSION_CANCELLED"
)

const defaultTimeout = 10 * time.Second

// Client talks to a single Reclaim backend base URL.
type Client struct {
	baseURL string
	hc      *http.Client
}

// New returns a Client for baseURL (trailing slashes trimmed). If baseURL is
// empty it defaults to the public production backend.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.reclaimprotocol.org"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: defaultTimeout},
	}
}

// SessionData mirrors the portal ReclaimSessionData (only the fields we use).
type SessionData struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	ProviderID string `json:"providerId"`
	// providerVersion is an object ({major,minor,patch}) in the real response;
	// providerVersionString is the pre-rendered "3.0.0" form. We prefer the
	// string field and fall back to the object.
	ProviderVersion    json.RawMessage   `json:"providerVersion"`
	ProviderVersionStr string            `json:"providerVersionString"`
	Status             string            `json:"status"`
	StatusV2           string            `json:"statusV2"`
	Proofs             []json.RawMessage `json:"proofs"`
	AppID              string            `json:"appId"`
	HTTPProviderID     []string          `json:"httpProviderId"`
}

// SessionResponse is the getSession envelope.
type SessionResponse struct {
	Message    string      `json:"message"`
	Session    SessionData `json:"session"`
	ProviderID string      `json:"providerId"`
}

// ProviderVersionString renders the provider version for the versionNumber
// query param. It prefers the pre-rendered providerVersionString field, then
// a string/number providerVersion, then a {major,minor,patch} object. Returns
// "" when no usable version is present.
func (s SessionData) ProviderVersionString() string {
	if s.ProviderVersionStr != "" {
		return s.ProviderVersionStr
	}
	if len(s.ProviderVersion) == 0 || string(s.ProviderVersion) == "null" {
		return ""
	}
	// string form: "3.0.0"
	var sv string
	if err := json.Unmarshal(s.ProviderVersion, &sv); err == nil {
		return sv
	}
	// object form: {"major":3,"minor":0,"patch":0}
	var obj struct {
		Major *int `json:"major"`
		Minor *int `json:"minor"`
		Patch *int `json:"patch"`
	}
	if err := json.Unmarshal(s.ProviderVersion, &obj); err == nil && obj.Major != nil {
		minor, patch := 0, 0
		if obj.Minor != nil {
			minor = *obj.Minor
		}
		if obj.Patch != nil {
			patch = *obj.Patch
		}
		return fmt.Sprintf("%d.%d.%d", *obj.Major, minor, patch)
	}
	// number form: 3
	var n json.Number
	if err := json.Unmarshal(s.ProviderVersion, &n); err == nil {
		return n.String()
	}
	return ""
}

// IsTerminal reports whether statusV2 means the session is already finished
// (proof submitted / cancelled), so we should not start a new flow.
func (s SessionData) IsTerminal() bool {
	switch s.StatusV2 {
	case StatusProofSubmitted, StatusSessionCancelled,
		"AI_PROOF_SUBMITTED", "PROOF_MANUAL_VERIFICATION_SUCCESS":
		return true
	}
	return false
}

// GetSession fetches session info for a sessionId.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*SessionResponse, error) {
	u := fmt.Sprintf("%s/api/sdk/session/%s", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Reclaim-Session-Id", sessionID)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getSession: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("session not found")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("getSession: status %d", resp.StatusCode)
	}
	var out SessionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("getSession: decode: %w", err)
	}
	return &out, nil
}

// GetProvider fetches the provider config via the unauthenticated providers
// endpoint and returns the first provider object verbatim (raw JSON), to be
// decoded into the caller's provider-config DTO. version may be "".
func (c *Client) GetProvider(ctx context.Context, providerID, version string) (json.RawMessage, error) {
	u := fmt.Sprintf("%s/api/providers/%s", c.baseURL, url.PathEscape(providerID))
	if version != "" {
		u += "?versionNumber=" + url.QueryEscape(version)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getProvider: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("getProvider: status %d", resp.StatusCode)
	}
	// "providers" is a single object on the unauthenticated /api/providers/{id}
	// endpoint, but an array on some other provider endpoints. Accept either.
	var env struct {
		Providers json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("getProvider: decode: %w", err)
	}
	trimmed := bytes.TrimSpace(env.Providers)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, fmt.Errorf("getProvider: no provider in response")
	}
	if trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("getProvider: decode array: %w", err)
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("getProvider: no provider in response")
		}
		return arr[0], nil
	}
	return trimmed, nil
}

// UpdateSessionRequest is the /api/sdk/update/session body.
type UpdateSessionRequest struct {
	SessionID       string         `json:"sessionId"`
	Status          string         `json:"status"`
	DeviceID        string         `json:"deviceId,omitempty"`
	DeviceType      string         `json:"deviceType,omitempty"`
	OSVersion       string         `json:"osVersion,omitempty"`
	PublicIPAddress string         `json:"publicIpAddress,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// UpdateSession posts a session-status transition. A 400 indicating the session
// is already in a final state is treated as non-fatal (returns nil), matching
// the portal's updateSession behavior.
func (c *Client) UpdateSession(ctx context.Context, r UpdateSessionRequest) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	u := c.baseURL + "/api/sdk/update/session"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Reclaim-Session-Id", r.SessionID)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("updateSession: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "final state") {
		return nil // already terminal — non-fatal
	}
	return fmt.Errorf("updateSession: status %d", resp.StatusCode)
}

// FeatureFlagBool queries a single boolean feature flag. Returns false on any
// error (matching featureFlag.ts fallbacks). appID/providerID may be "".
func (c *Client) FeatureFlagBool(ctx context.Context, name, appID, providerID string) bool {
	q := url.Values{}
	q.Set("featureFlagNames", name)
	if appID != "" {
		q.Set("appId", appID)
	}
	if providerID != "" {
		q.Set("providerId", providerID)
	}
	u := c.baseURL + "/api/feature-flags/get?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var arr []struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(body, &arr); err != nil || len(arr) == 0 {
		return false
	}
	v, _ := arr[0].Value.(bool)
	return v
}

// NOTE: proof callback submission (POST /api/sdk/callback) is intentionally NOT
// implemented here — the image never submits proofs. That remains a gateway
// responsibility; the image only exposes proofs via GET /session/claim.
