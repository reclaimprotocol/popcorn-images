package egressproxy

import "testing"

// Dummy template — NOT real credentials. Mirrors the BrightData URL shape only.
const tmpl = "http://brd-customer-EXAMPLE-zone-test-country-{{geoLocation}}:testpass@brd.superproxy.io:33335"

func TestParseAndSessionUsername(t *testing.T) {
	p, err := Parse(tmpl, "US")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}
	if p.Scheme != "http" || p.Host != "brd.superproxy.io" || p.Port != 33335 {
		t.Fatalf("host/port/scheme: %+v", p)
	}
	// {{geoLocation}} lowercased into the username, matching reclaim-tee.
	if want := "brd-customer-EXAMPLE-zone-test-country-us"; p.Username != want {
		t.Fatalf("username=%q want %q", p.Username, want)
	}
	if p.Password != "testpass" {
		t.Fatalf("password=%q", p.Password)
	}
	if got := p.SessionUsername("sess-123"); got != "brd-customer-EXAMPLE-zone-test-country-us-session-sess-123" {
		t.Fatalf("session username=%q", got)
	}
	if got := p.SessionUsername(""); got != p.Username {
		t.Fatalf("empty session should be base username, got %q", got)
	}
}

func TestParseDisabledAndInvalid(t *testing.T) {
	if p, err := Parse("", "US"); p != nil || err != nil {
		t.Fatalf("empty template => (nil,nil), got (%v,%v)", p, err)
	}
	if _, err := Parse("http://user:pass@host:8080", "US"); err == nil {
		t.Fatal("expected error when {{geoLocation}} placeholder missing")
	}
}

func TestNormalizeCountry(t *testing.T) {
	cases := map[string]string{"": "IN", "{{DYNAMIC_GEO}}": "IN", "us": "US", "JP": "JP", " in ": "IN", "USA": "IN"}
	for in, want := range cases {
		if g := NormalizeCountry(in, "IN"); g != want {
			t.Errorf("NormalizeCountry(%q,IN)=%q want %q", in, g, want)
		}
	}
	// empty fallback => unresolvable geo normalizes to "" (caller skips proxy)
	for _, in := range []string{"", "{{DYNAMIC_GEO}}", "USA", " "} {
		if g := NormalizeCountry(in, ""); g != "" {
			t.Errorf("NormalizeCountry(%q,\"\")=%q want \"\"", in, g)
		}
	}
	if g := NormalizeCountry("us", ""); g != "US" {
		t.Errorf("valid geo dropped: %q", g)
	}
}
