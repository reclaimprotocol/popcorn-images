package browser

import (
	"regexp"
	"strings"
)

// extractURLParams pulls {{var}} values out of url given its template. Each
// template variable becomes a capture group (greedy for "GRD"-suffixed names,
// else lazy), matching the portal worker's parameter extraction.
func extractURLParams(template, url string) map[string]string {
	out := map[string]string{}
	tokens := tmplVar.FindAllStringSubmatchIndex(template, -1)
	if len(tokens) == 0 {
		return out
	}
	var b strings.Builder
	b.WriteString("(?s)^")
	var names []string
	last := 0
	for _, idx := range tokens {
		b.WriteString(regexp.QuoteMeta(template[last:idx[0]]))
		name := template[idx[2]:idx[3]]
		names = append(names, name)
		if strings.HasSuffix(name, "GRD") {
			b.WriteString("(.*)")
		} else {
			b.WriteString("(.*?)")
		}
		last = idx[1]
	}
	b.WriteString(regexp.QuoteMeta(template[last:]))
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return out
	}
	m := re.FindStringSubmatch(url)
	if m == nil {
		return out
	}
	for i, name := range names {
		if i+1 < len(m) {
			out[name] = m[i+1]
		}
	}
	return out
}

// separateSecrets splits params into public and secret by the portal worker's
// rule: any key whose uppercase form contains "SECRET" is secret.
func separateSecrets(params map[string]string) (public, secret map[string]string) {
	public = map[string]string{}
	secret = map[string]string{}
	for k, v := range params {
		if strings.Contains(strings.ToUpper(k), "SECRET") {
			secret[k] = v
		} else {
			public[k] = v
		}
	}
	return public, secret
}

// headerLookup returns the value of a header by case-insensitive name.
func headerLookup(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
