package browser

import "testing"

func TestMatchesURL(t *testing.T) {
	cases := []struct {
		name    string
		matcher RequestMatcher
		url     string
		want    bool
	}{
		{"constant equal", RequestMatcher{URL: "https://x.com/a", URLType: "CONSTANT"}, "https://x.com/a", true},
		{"constant differ", RequestMatcher{URL: "https://x.com/a", URLType: "CONSTANT"}, "https://x.com/b", false},
		{"template var", RequestMatcher{URL: "https://x.com/u/{{id}}/p"}, "https://x.com/u/42/p", true},
		{"template anchored", RequestMatcher{URL: "https://x.com/u/{{id}}"}, "https://x.com/u/42/extra/no", true},
		{"template no match", RequestMatcher{URL: "https://x.com/u/{{id}}/p"}, "https://y.com/u/42/p", false},
		{"regex", RequestMatcher{URL: `https://x\.com/u/\d+`, URLType: "REGEX"}, "https://x.com/u/99", true},
	}
	for _, c := range cases {
		if got := matchesURL(c.matcher, c.url, nil); got != c.want {
			t.Errorf("%s: matchesURL=%v want %v", c.name, got, c.want)
		}
	}
}

func TestExtractURLParams(t *testing.T) {
	got := extractURLParams("https://x.com/u/{{id}}/p/{{slug}}", "https://x.com/u/42/p/hello")
	if got["id"] != "42" {
		t.Errorf("id = %q, want 42", got["id"])
	}
	if got["slug"] != "hello" {
		t.Errorf("slug = %q, want hello", got["slug"])
	}
}

func TestSeparateSecrets(t *testing.T) {
	pub, secret := separateSecrets(map[string]string{
		"userId":     "1",
		"apiSecret":  "shh",
		"SECRET_KEY": "k",
	})
	if len(pub) != 1 || pub["userId"] != "1" {
		t.Errorf("public = %v, want {userId:1}", pub)
	}
	if len(secret) != 2 {
		t.Errorf("secret = %v, want 2 entries", secret)
	}
}

func TestBuildProviderParamsRejectsOPRFRaw(t *testing.T) {
	m := RequestMatcher{
		URL:                "https://x.com/a",
		ResponseRedactions: []ResponseRedaction{{Regex: "x", Hash: "oprf-raw"}},
	}
	if _, err := buildProviderParamsJSON(m, capturedForProof{URL: "https://x.com/a"}); err == nil {
		t.Fatal("expected oprf-raw to be rejected")
	}
}
