package browser

import (
	"strings"
	"testing"
	"time"
)

func TestJsSelectorEscapes(t *testing.T) {
	got := jsSelector(`a"b\c`)
	want := `"a\"b\\c"`
	if got != want {
		t.Fatalf("jsSelector = %s, want %s", got, want)
	}
}

func TestJsHelpersEmbedSelector(t *testing.T) {
	for name, fn := range map[string]func(string) string{
		"jsExists":            jsExists,
		"jsVisible":           jsVisible,
		"jsCenterAfterScroll": jsCenterAfterScroll,
		"jsFocusAndClear":     jsFocusAndClear,
		"jsClick":             jsClick,
	} {
		expr := fn(`#login > input[name="user"]`)
		if !strings.Contains(expr, `querySelector(`) {
			t.Errorf("%s: expected a querySelector call, got %q", name, expr)
		}
		// Selector must be embedded as a JSON-encoded literal (quotes escaped).
		if !strings.Contains(expr, `name=\"user\"`) {
			t.Errorf("%s: selector not safely encoded in %q", name, expr)
		}
	}
}

func TestMsOrDefault(t *testing.T) {
	if got := msOrDefault(nil, 100); got != 100*time.Millisecond {
		t.Errorf("nil -> %v, want 100ms", got)
	}
	v := 5
	if got := msOrDefault(&v, 100); got != 5*time.Millisecond {
		t.Errorf("&5 -> %v, want 5ms", got)
	}
	neg := -1
	if got := msOrDefault(&neg, 100); got != 100*time.Millisecond {
		t.Errorf("negative -> %v, want default 100ms", got)
	}
}
