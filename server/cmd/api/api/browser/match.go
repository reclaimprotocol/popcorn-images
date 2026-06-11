package browser

import (
	"encoding/json"
	"regexp"
	"strings"
)

// tmplVar matches a {{name}} template variable.
var tmplVar = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// bodyTmplVar matches a {{...}} token in a responseMatch value (lazy, mirrors
// the portal's templateToRegex which replaces /\{\{.*?\}\}/g with (.+?)).
var bodyTmplVar = regexp.MustCompile(`\{\{.*?\}\}`)

// matchesURL reports whether url satisfies the matcher. Mirrors the portal
// worker's URL matching: EXACT/CONSTANT use equality; REGEX/TEMPLATE (or empty
// type, inferred) compile to a fully-anchored pattern. Method/body matching is
// intentionally not performed (the portal primary path was URL-only).
func matchesURL(m RequestMatcher, url string, knownParams map[string]string) bool {
	switch strings.ToUpper(m.URLType) {
	case "EXACT", "CONSTANT":
		return canonicalizeMatchURL(url) == canonicalizeMatchURL(m.URL)
	case "REGEX":
		// REGEX patterns use `?`/`/` as metacharacters, so don't canonicalize the
		// pattern; only the captured URL is already browser-canonical.
		re, err := regexp.Compile("(?s)^" + m.URL + "$")
		return err == nil && re.MatchString(url)
	default: // TEMPLATE or unspecified
		re, err := templateToRegex(canonicalizeMatchURL(m.URL), knownParams)
		return err == nil && re.MatchString(canonicalizeMatchURL(url))
	}
}

// canonicalizeMatchURL inserts the implicit "/" between the authority and the
// query/fragment when a provider writes a path-less URL like
// "https://api.ipify.org?format=json". Browsers (and the WHATWG URL spec) emit
// "https://api.ipify.org/?format=json" for the same fetch, so without this the
// matcher's literal "?" sits right after the host and never matches the
// captured request. Only the host→(?|#) boundary is touched; URLs that already
// have a path are returned unchanged. Not applied to REGEX matchers.
func canonicalizeMatchURL(s string) string {
	i := strings.Index(s, "://")
	if i < 0 {
		return s
	}
	authStart := i + 3
	rest := s[authStart:]
	j := strings.IndexAny(rest, "/?#")
	if j < 0 {
		return s + "/" // bare scheme://host → scheme://host/
	}
	if rest[j] == '/' {
		return s // already has a path
	}
	return s[:authStart+j] + "/" + rest[j:] // insert "/" before ? or #
}

// templateToRegex converts a {{var}} template into an anchored regex. Known
// params are substituted as escaped literals; unknown vars become capture
// groups — greedy (.*) when the name ends in "GRD", else lazy (.*?).
func templateToRegex(template string, knownParams map[string]string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?s)^")
	last := 0
	for _, idx := range tmplVar.FindAllStringSubmatchIndex(template, -1) {
		b.WriteString(regexp.QuoteMeta(template[last:idx[0]]))
		name := template[idx[2]:idx[3]]
		if v, ok := knownParams[name]; ok {
			b.WriteString(regexp.QuoteMeta(v))
		} else if strings.HasSuffix(name, "GRD") {
			b.WriteString("(.*)")
		} else {
			b.WriteString("(.*?)")
		}
		last = idx[1]
	}
	b.WriteString(regexp.QuoteMeta(template[last:]))
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// --- response method + body matching --------------------------------------
//
// Ports the portal's shared/response-matching.ts: a request only triggers a
// proof when its method matches AND its response body satisfies every
// responseMatches/responseRedactions rule. URL match alone is not enough — the
// same URL can return a logged-out shell before login and the real payload
// after, so a body that doesn't match means "wait for a later response", not
// "fail".

// matchesMethod reports whether the request method satisfies the matcher. An
// empty matcher method matches any method (the portal treats it as a wildcard).
func matchesMethod(m RequestMatcher, method string) bool {
	if m.Method == "" {
		return true
	}
	return strings.EqualFold(m.Method, method)
}

// responseMatchRule / responseRedactionRule are the gating subsets of the
// portal rule shapes. Only the fields needed to decide a match are decoded; the
// full raw JSON is still forwarded to reclaim-tee unchanged.
type responseMatchRule struct {
	Value      string `json:"value"`
	Type       string `json:"type"`
	Invert     bool   `json:"invert"`
	IsOptional bool   `json:"isOptional"`
}

type responseRedactionRule struct {
	Regex string `json:"regex"`
}

// compileBodyRegex compiles pattern with the portal's "is" flags
// (case-insensitive + dotAll). Empty/invalid patterns yield nil.
func compileBodyRegex(pattern string) *regexp.Regexp {
	if strings.TrimSpace(pattern) == "" {
		return nil
	}
	re, err := regexp.Compile("(?is)" + pattern)
	if err != nil {
		return nil
	}
	return re
}

// templateToBodyRegex turns a {{var}} response-match value into a regex source,
// escaping literals and replacing each {{...}} with (.+?) (portal parity).
func templateToBodyRegex(tmpl string) string {
	parts := bodyTmplVar.Split(tmpl, -1)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return strings.Join(parts, "(.+?)")
}

// evaluateRule mirrors the portal's evaluateRule. It returns whether the rule
// matched and whether it should be skipped (null/optional → not decisive).
func evaluateRule(body string, match *responseMatchRule, red *responseRedactionRule) (matched, skip bool) {
	var re *regexp.Regexp
	if red != nil {
		re = compileBodyRegex(red.Regex)
	}
	if re == nil && match != nil && strings.EqualFold(match.Type, "regex") && match.Value != "" {
		re = compileBodyRegex(match.Value)
	}
	if re == nil && match != nil && strings.Contains(match.Value, "{{") {
		re = compileBodyRegex(templateToBodyRegex(match.Value))
	}

	var isMatch *bool
	switch {
	case re != nil:
		v := re.MatchString(body)
		isMatch = &v
	case match != nil && match.Value != "":
		v := strings.Contains(body, match.Value)
		isMatch = &v
	}
	if isMatch == nil {
		return false, true // nothing to test → skip
	}

	res := *isMatch
	if match != nil && match.Invert {
		res = !res
	}
	if !res && match != nil && match.IsOptional {
		return false, true // optional miss → skip
	}
	return res, false
}

// responseBodyMatches reports whether body satisfies the matcher's
// responseMatches/responseRedactions. Mirrors the portal's
// evaluateResponseMatcher: a matcher with neither matches nor redactions never
// matches (nothing to verify), and every decisive rule must pass.
func responseBodyMatches(m RequestMatcher, body string) bool {
	var matches []responseMatchRule
	var reds []responseRedactionRule
	if len(m.ResponseMatches) > 0 {
		_ = json.Unmarshal(m.ResponseMatches, &matches)
	}
	if len(m.ResponseRedactions) > 0 {
		_ = json.Unmarshal(m.ResponseRedactions, &reds)
	}
	if len(matches) == 0 && len(reds) == 0 {
		return false
	}

	total := len(matches)
	if len(reds) > total {
		total = len(reds)
	}
	for i := 0; i < total; i++ {
		var mm *responseMatchRule
		if i < len(matches) {
			mm = &matches[i]
		}
		var rr *responseRedactionRule
		if i < len(reds) {
			rr = &reds[i]
		}
		matched, skip := evaluateRule(body, mm, rr)
		if skip {
			continue
		}
		if !matched {
			return false
		}
	}
	return true
}
