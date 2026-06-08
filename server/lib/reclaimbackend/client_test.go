package reclaimbackend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProviderVersionString locks in the version-rendering fix: the real
// getSession response carries providerVersion as an object plus a separate
// providerVersionString field. We must never emit the raw object blob as a
// versionNumber (the backend rejects it with 400 Invalid version number format).
func TestProviderVersionString(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"prefers providerVersionString", `{"providerVersionString":"3.0.0","providerVersion":{"major":9}}`, "3.0.0"},
		{"object form", `{"providerVersion":{"major":3,"minor":0,"patch":0}}`, "3.0.0"},
		{"object partial", `{"providerVersion":{"major":2}}`, "2.0.0"},
		{"string form", `{"providerVersion":"1.2.3"}`, "1.2.3"},
		{"number form", `{"providerVersion":4}`, "4"},
		{"null", `{"providerVersion":null}`, ""},
		{"absent", `{}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sd SessionData
			if err := json.Unmarshal([]byte(tc.json), &sd); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := sd.ProviderVersionString(); got != tc.want {
				t.Errorf("ProviderVersionString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetSession(t *testing.T) {
	// Shape mirrors the real /api/sdk/session/{id} response.
	const resp = `{"message":"ok","session":{"id":"x","sessionId":"s1","providerId":"example",` +
		`"providerVersionString":"3.0.0","providerVersion":{"major":3,"minor":0,"patch":0},` +
		`"statusV2":"SESSION_STARTED","appId":"0xABC","proofs":[]},"providerId":"example"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Reclaim-Session-Id"); got != "s1" {
			t.Errorf("missing/wrong session header: %q", got)
		}
		if r.URL.Path != "/api/sdk/session/s1" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	out, err := New(srv.URL).GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if out.Session.AppID != "0xABC" || out.Session.ProviderID != "example" {
		t.Errorf("unexpected session: %+v", out.Session)
	}
	if v := out.Session.ProviderVersionString(); v != "3.0.0" {
		t.Errorf("version = %q, want 3.0.0", v)
	}
	if out.Session.IsTerminal() {
		t.Error("SESSION_STARTED should not be terminal")
	}
}

func TestGetSessionNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := New(srv.URL).GetSession(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("want 'session not found', got %v", err)
	}
}

// TestGetProviderObjectForm locks in the fix: the unauthenticated
// /api/providers/{id} endpoint returns providers as a single object, not an
// array. The client must return that object verbatim.
func TestGetProviderObjectForm(t *testing.T) {
	const resp = `{"message":"ok","providers":{"providerId":"example","loginUrl":"https://example.org/","requestData":[{},{},{}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("versionNumber") != "3.0.0" {
			t.Errorf("unexpected versionNumber: %q", r.URL.Query().Get("versionNumber"))
		}
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	raw, err := New(srv.URL).GetProvider(context.Background(), "example", "3.0.0")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	var dto struct {
		ProviderID  string            `json:"providerId"`
		LoginURL    string            `json:"loginUrl"`
		RequestData []json.RawMessage `json:"requestData"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if dto.LoginURL != "https://example.org/" || len(dto.RequestData) != 3 {
		t.Errorf("unexpected provider: %+v", dto)
	}
}

// TestGetProviderArrayForm keeps the array-shaped fallback working.
func TestGetProviderArrayForm(t *testing.T) {
	const resp = `{"providers":[{"providerId":"a","loginUrl":"https://a/"},{"providerId":"b"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	raw, err := New(srv.URL).GetProvider(context.Background(), "a", "")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	var dto struct {
		ProviderID string `json:"providerId"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ProviderID != "a" {
		t.Errorf("want first provider 'a', got %q", dto.ProviderID)
	}
}

func TestGetProviderEmpty(t *testing.T) {
	for _, body := range []string{`{"providers":[]}`, `{"providers":null}`, `{}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := New(srv.URL).GetProvider(context.Background(), "x", "")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "no provider") {
			t.Errorf("body %s: want 'no provider' error, got %v", body, err)
		}
	}
}

func TestUpdateSessionSwallowsFinalState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"session already in final state"}`))
	}))
	defer srv.Close()
	err := New(srv.URL).UpdateSession(context.Background(), UpdateSessionRequest{SessionID: "s1", Status: StatusProofGenerationSuccess})
	if err != nil {
		t.Fatalf("final-state 400 should be non-fatal, got %v", err)
	}
}

func TestUpdateSessionOtherErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"missing field"}`))
	}))
	defer srv.Close()
	err := New(srv.URL).UpdateSession(context.Background(), UpdateSessionRequest{SessionID: "s1", Status: "X"})
	if err == nil {
		t.Fatal("non-final-state 400 should be an error")
	}
}

func TestFeatureFlagBool(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"true", 200, `[{"value":true}]`, true},
		{"false", 200, `[{"value":false}]`, false},
		{"empty array", 200, `[]`, false},
		{"non-bool value", 200, `[{"value":"yes"}]`, false},
		{"server error", 500, ``, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("featureFlagNames") != "usePortalTEE" {
					t.Errorf("missing flag name query")
				}
				if tc.status != 200 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if got := New(srv.URL).FeatureFlagBool(context.Background(), "usePortalTEE", "0xABC", "example"); got != tc.want {
				t.Errorf("FeatureFlagBool = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []string{StatusProofSubmitted, StatusSessionCancelled, "AI_PROOF_SUBMITTED", "PROOF_MANUAL_VERIFICATION_SUCCESS"}
	for _, s := range terminal {
		if !(SessionData{StatusV2: s}).IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []string{"SESSION_STARTED", StatusProofGenerationStarted, ""} {
		if (SessionData{StatusV2: s}).IsTerminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}
