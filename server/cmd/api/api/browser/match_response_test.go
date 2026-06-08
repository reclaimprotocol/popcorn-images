package browser

import (
	"encoding/json"
	"testing"
)

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestMatchesMethod(t *testing.T) {
	if !matchesMethod(RequestMatcher{Method: ""}, "GET") {
		t.Error("empty matcher method should match any method")
	}
	if !matchesMethod(RequestMatcher{Method: "get"}, "GET") {
		t.Error("method match should be case-insensitive")
	}
	if matchesMethod(RequestMatcher{Method: "POST"}, "GET") {
		t.Error("POST matcher should not match GET")
	}
}

// TestResponseBodyMatches is the core regression: a URL match alone must not be
// enough. The same provider URL returns a logged-out body (no username) and a
// logged-in body (with username); only the latter should satisfy the matcher.
func TestResponseBodyMatches(t *testing.T) {
	// A redaction regex extracting "username", like a real provider.
	m := RequestMatcher{
		ResponseRedactions: rawJSON(t, []map[string]any{
			{"regex": `"username"\s*:\s*"(.*?)"`},
		}),
		ResponseMatches: rawJSON(t, []map[string]any{
			{"value": "{{username}}", "type": "contains"},
		}),
	}

	loggedOut := `{"user": null, "ads": [1,2,3]}`
	loggedIn := `{"user": {"username": "kryptocodes"}}`

	if responseBodyMatches(m, loggedOut) {
		t.Error("logged-out body (no username) must NOT match — should wait, not fail")
	}
	if !responseBodyMatches(m, loggedIn) {
		t.Error("logged-in body (has username) must match")
	}
}

func TestResponseBodyMatches_EmptyRules(t *testing.T) {
	// No matches and no redactions → nothing to verify → never matches.
	if responseBodyMatches(RequestMatcher{}, "anything") {
		t.Error("matcher with no rules should not match")
	}
}

func TestResponseBodyMatches_LiteralContains(t *testing.T) {
	m := RequestMatcher{
		ResponseMatches: rawJSON(t, []map[string]any{
			{"value": "logged in as", "type": "contains"},
		}),
	}
	if !responseBodyMatches(m, "you are logged in as Bret") {
		t.Error("literal substring should match")
	}
	if responseBodyMatches(m, "please sign in") {
		t.Error("missing literal should not match")
	}
}

func TestResponseBodyMatches_Optional(t *testing.T) {
	// Optional rule that misses is skipped; a present required rule still decides.
	m := RequestMatcher{
		ResponseMatches: rawJSON(t, []map[string]any{
			{"value": "missing-token", "type": "contains", "isOptional": true},
			{"value": "present-token", "type": "contains"},
		}),
	}
	if !responseBodyMatches(m, "body has present-token only") {
		t.Error("optional miss should be skipped; required hit should pass")
	}
}

func TestResponseBodyMatches_Invert(t *testing.T) {
	// invert: matches when the value is ABSENT.
	m := RequestMatcher{
		ResponseMatches: rawJSON(t, []map[string]any{
			{"value": "error", "type": "contains", "invert": true},
		}),
	}
	if !responseBodyMatches(m, "all good") {
		t.Error("inverted rule should match when value absent")
	}
	if responseBodyMatches(m, "an error occurred") {
		t.Error("inverted rule should not match when value present")
	}
}

func TestResponseBodyMatches_CaseInsensitiveDotAll(t *testing.T) {
	// "(?is)" — case-insensitive and . matches newline (portal REGEX_FLAGS='is').
	m := RequestMatcher{
		ResponseRedactions: rawJSON(t, []map[string]any{
			{"regex": `NAME.*VALUE`},
		}),
	}
	if !responseBodyMatches(m, "name\nfoo\nvalue") {
		t.Error("regex should be case-insensitive and dot-all")
	}
}

func TestMatchProof_WaitsForCorrectBody(t *testing.T) {
	nc := &NetCapture{
		matchers: []RequestMatcher{{
			URL:     "https://api.example.com/me",
			URLType: "EXACT",
			Method:  "GET",
			ResponseRedactions: rawJSON(t, []map[string]any{
				{"regex": `"username"\s*:\s*"(.*?)"`},
			}),
		}},
	}
	pr := &pendingRequest{URL: "https://api.example.com/me", Method: "GET"}

	if idx := nc.matchProof(pr, `{"user":null}`); idx != -1 {
		t.Errorf("logged-out body should not match (got idx %d)", idx)
	}
	if idx := nc.matchProof(pr, `{"username":"bret"}`); idx != 0 {
		t.Errorf("logged-in body should match matcher 0 (got idx %d)", idx)
	}
	// Wrong method, right URL+body → no match.
	prPost := &pendingRequest{URL: "https://api.example.com/me", Method: "POST"}
	if idx := nc.matchProof(prPost, `{"username":"bret"}`); idx != -1 {
		t.Errorf("method mismatch should not match (got idx %d)", idx)
	}
}

// TestProofRetrySemantics locks in: a failed proof attempt does NOT consume the
// matcher — a later, different request (e.g. same API after login → different
// cookie) gets a fresh attempt, and success closes the matcher. The identical
// request is never re-attempted.
func TestProofRetrySemantics(t *testing.T) {
	nc := &NetCapture{matchers: []RequestMatcher{{}}, report: func(string, map[string]any) {}}

	// Logged-out request (no cookie) — first attempt allowed.
	loggedOut := &pendingRequest{URL: "u", Method: "GET", ReqHeaders: map[string]string{}}
	h1 := requestHash(loggedOut)
	if !nc.beginAttempt(0, h1) {
		t.Fatal("first attempt should be allowed")
	}
	if nc.InFlight() != 1 {
		t.Errorf("in-flight = %d, want 1", nc.InFlight())
	}
	// It fails — matcher must remain open, nothing succeeded.
	nc.finishAttempt(0, false)
	if nc.SucceededCount() != 0 {
		t.Errorf("succeeded = %d after failure, want 0", nc.SucceededCount())
	}
	if nc.InFlight() != 0 {
		t.Errorf("in-flight = %d after finish, want 0", nc.InFlight())
	}

	// The SAME request must not be retried (dedup).
	if nc.beginAttempt(0, h1) {
		t.Error("identical request should not be re-attempted")
	}

	// A different request (post-login: cookie present → different hash) retries.
	loggedIn := &pendingRequest{URL: "u", Method: "GET", ReqHeaders: map[string]string{"cookie": "session=abc"}}
	h2 := requestHash(loggedIn)
	if h2 == h1 {
		t.Fatal("cookie change must change the request hash")
	}
	if !nc.beginAttempt(0, h2) {
		t.Fatal("post-login request should get a fresh attempt")
	}
	nc.finishAttempt(0, true) // succeeds
	if nc.SucceededCount() != 1 {
		t.Errorf("succeeded = %d after success, want 1", nc.SucceededCount())
	}

	// Once proved, even a brand-new matching request is not attempted again.
	other := &pendingRequest{URL: "u", Method: "GET", ReqHeaders: map[string]string{"cookie": "session=xyz"}}
	if nc.beginAttempt(0, requestHash(other)) {
		t.Error("proved matcher should not be attempted again")
	}
}
