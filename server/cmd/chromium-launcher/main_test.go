package main

import "testing"

func TestNormalizeStartupURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "https", raw: "https://www.google.com", want: "https://www.google.com"},
		{name: "http", raw: "http://example.com/path", want: "http://example.com/path"},
		{name: "trimmed", raw: "  https://example.com  ", want: "https://example.com"},
		{name: "flag", raw: "--disable-web-security", want: ""},
		{name: "unsupported scheme", raw: "file:///tmp/index.html", want: ""},
		{name: "missing host", raw: "https:///missing-host", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeStartupURL(tt.raw); got != tt.want {
				t.Fatalf("normalizeStartupURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
