package browser

import "encoding/json"

// jsSelector safely encodes a CSS selector as a JS string literal so it can be
// embedded in an evaluate expression without quoting/injection bugs.
func jsSelector(selector string) string {
	b, _ := json.Marshal(selector)
	return string(b)
}

// jsExists returns an expression evaluating to true when the selector matches a
// node in the DOM (Playwright state:"attached" — presence, not visibility).
func jsExists(selector string) string {
	return "!!document.querySelector(" + jsSelector(selector) + ")"
}

// jsVisible returns an expression evaluating to true when the selector matches a
// visible, non-zero-area element. Mirrors the portal worker's visibility check.
func jsVisible(selector string) string {
	sel := jsSelector(selector)
	return "(() => { const el = document.querySelector(" + sel + "); if (!el) return false;" +
		" const s = getComputedStyle(el), r = el.getBoundingClientRect();" +
		" return s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0' && r.width > 0 && r.height > 0; })()"
}

// jsCenterAfterScroll scrolls the element into view and returns its viewport
// center as {x, y}, or null when the selector doesn't match.
func jsCenterAfterScroll(selector string) string {
	sel := jsSelector(selector)
	return "(() => { const el = document.querySelector(" + sel + "); if (!el) return null;" +
		" el.scrollIntoView({block:'center', inline:'center'});" +
		" const r = el.getBoundingClientRect();" +
		" return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()"
}

// jsFocusAndClear focuses the element and clears its value, returning true on
// success (false when the selector doesn't match).
func jsFocusAndClear(selector string) string {
	sel := jsSelector(selector)
	return "(() => { const el = document.querySelector(" + sel + "); if (!el) return false;" +
		" el.focus(); if ('value' in el) el.value = ''; return true; })()"
}

// jsClick invokes the element's native .click() (the visibility-failed fallback
// path).
func jsClick(selector string) string {
	sel := jsSelector(selector)
	return "(() => { const el = document.querySelector(" + sel + "); if (!el) return false; el.click(); return true; })()"
}
