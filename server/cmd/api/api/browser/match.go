package browser

import (
	"regexp"
	"strings"
)

// tmplVar matches a {{name}} template variable.
var tmplVar = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// matchesURL reports whether url satisfies the matcher. Mirrors the portal
// worker's URL matching: EXACT/CONSTANT use equality; REGEX/TEMPLATE (or empty
// type, inferred) compile to a fully-anchored pattern. Method/body matching is
// intentionally not performed (the portal primary path was URL-only).
func matchesURL(m RequestMatcher, url string, knownParams map[string]string) bool {
	switch strings.ToUpper(m.URLType) {
	case "EXACT", "CONSTANT":
		return url == m.URL
	case "REGEX":
		re, err := regexp.Compile("(?s)^" + m.URL + "$")
		return err == nil && re.MatchString(url)
	default: // TEMPLATE or unspecified
		re, err := templateToRegex(m.URL, knownParams)
		return err == nil && re.MatchString(url)
	}
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
