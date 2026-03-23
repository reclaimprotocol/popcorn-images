/*
 * extraction.go — Match captured browser requests against provider patterns,
 * extract parameters, and build the provider_params_json for TEE attestation.
 *
 */
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/onkernel/kernel-images/server/lib/logger"
	xp "github.com/reclaimprotocol/xpath-go"
)

/*
 * Wire types — what the daemon sends us, what we send back.
 */

type MatchRequest struct {
	URL             string            `json:"captured_url"`
	Method          string            `json:"captured_method"`
	Headers         map[string]string `json:"captured_headers"`
	Body            json.RawMessage   `json:"captured_body,omitempty"`
	ResponseBody    string            `json:"response_body"`
	ResponseCT      string            `json:"response_content_type,omitempty"`
	Pattern         ProviderPattern   `json:"pattern"`
	Params          map[string]string `json:"parameters,omitempty"`
	Geo             string            `json:"geo_location,omitempty"`
}

type ProviderPattern struct {
	URL        string           `json:"url"`
	Method     string           `json:"method"`
	Matches    []MatchRule      `json:"responseMatches,omitempty"`
	Redactions []RedactionRule  `json:"responseRedactions,omitempty"`
	BodySniff  *BodySniffRule   `json:"bodySniff,omitempty"`
	Variables  []string         `json:"responseVariables,omitempty"`
}

type MatchRule struct {
	Value    string `json:"value"`
	Type     string `json:"type"` // "contains" or "regex"
	Invert   bool   `json:"invert,omitempty"`
	Optional bool   `json:"isOptional,omitempty"`
}

type RedactionRule struct {
	XPath    string `json:"xPath,omitempty"`
	JSONPath string `json:"jsonPath,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

type BodySniffRule struct {
	Enabled  bool   `json:"enabled"`
	Template string `json:"template,omitempty"`
}

type MatchResult struct {
	Matched    bool              `json:"matched"`
	Params     map[string]string `json:"extracted_params,omitempty"`
	ProveJSON  string            `json:"provider_params_json,omitempty"`
	Error      string            `json:"error,omitempty"`
}

/*
 * HandleMatch — the only entry point. One request in, one answer out.
 *
 * POST /browser-events/match
 */
func (s *ApiService) HandleMatch(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context())

	var req MatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, MatchResult{Error: "bad json"})
		return
	}

	body := bodyToString(req.Body)

	/* Phase 1: gather all known parameter values */
	params := mergeParams(
		req.Params,
		extractFromURL(req.Pattern.URL, req.URL),
		extractFromBody(body, req.Pattern.BodySniff),
		extractFromResponse(req.ResponseBody, req.Pattern.Redactions, req.Pattern.Variables, req.Params, log),
	)

	/* Phase 2: check every responseMatch against the response */
	failed := checkMatches(req.ResponseBody, req.Pattern.Matches, params)
	if failed != nil {
		if failed.hard {
			respondJSON(w, http.StatusOK, MatchResult{Matched: false, Params: params, Error: failed.reason})
			return
		}
		/* optional failure — just note it, we'll filter later */
	}

	/* Phase 3: remove failed optional matches and their redactions */
	matches, redactions := filterFailed(req.Pattern.Matches, req.Pattern.Redactions)

	/* Phase 4: build the provider_params_json the TEE expects */
	prove := buildProvePayload(req, body, params, matches, redactions)
	out, _ := json.Marshal(prove)

	log.Info("matched", "url", req.URL, "params", fmt.Sprintf("%v", params))
	respondJSON(w, http.StatusOK, MatchResult{
		Matched:   true,
		Params:    params,
		ProveJSON: string(out),
	})
}

/* ═══════════════════════════════════════════════════════════════════
 * PARAMETER EXTRACTION
 *
 * Each function extracts from one source. mergeParams combines them.
 * Later sources override earlier ones.
 * ═══════════════════════════════════════════════════════════════════ */

func mergeParams(sources ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, src := range sources {
		for k, v := range src {
			out[k] = v
		}
	}
	return out
}

/*
 * extractFromURL — match {{var}} placeholders in the provider URL template
 * against the actual captured URL.
 *
 * Template:  "https://api.com/{{userId}}/data"
 * Captured:  "https://api.com/456/data"
 * Result:    {"userId": "456"}
 */
func extractFromURL(template, actual string) map[string]string {
	return matchTemplate(template, actual)
}

/*
 * extractFromBody — match {{var}} placeholders in the bodySniff template
 * against the actual request body.
 */
func extractFromBody(body string, sniff *BodySniffRule) map[string]string {
	if sniff == nil || !sniff.Enabled || sniff.Template == "" {
		return nil
	}
	return matchTemplate(sniff.Template, body)
}

/*
 * extractFromResponse — the big one. Walk each redaction rule, extract
 * the element via XPath or JSONPath, then apply regex.
 *
 * Parameters accumulate: redaction[1] can use values extracted by redaction[0].
 */
func extractFromResponse(
	body string,
	redactions []RedactionRule,
	varNames []string,
	seed map[string]string,
	log interface{ Warn(string, ...any) },
) map[string]string {
	out := make(map[string]string)
	acc := copyMap(seed)

	for i, rule := range redactions {
		elem := body

		/* XPath: narrow down to the HTML element */
		if rule.XPath != "" {
			val := evalXPath(subst(rule.XPath, acc), elem, rule.Regex)
			if val == "" {
				continue
			}
			elem = val
		}

		/* JSONPath: narrow down to the JSON field */
		if rule.JSONPath != "" && rule.XPath == "" {
			val := evalJSONPath(elem, subst(rule.JSONPath, acc))
			if val == "" {
				continue
			}
			/* JSONPath already gives us the final value — store it directly */
			name := varAt(varNames, i)
			if name != "" {
				out[name] = val
				acc[name] = val
			}
			continue
		}

		/* Regex: extract capture group from the element */
		if rule.Regex != "" {
			captures := evalRegex(subst(rule.Regex, acc), elem)
			name := varAt(varNames, i)
			if name != "" && captures["1"] != "" {
				out[name] = captures["1"]
				acc[name] = captures["1"]
			}
			for k, v := range captures {
				if k != "1" && v != "" {
					out[k] = v
					acc[k] = v
				}
			}
		}
	}
	return out
}

/* ═══════════════════════════════════════════════════════════════════
 * RESPONSE MATCHING
 * ═══════════════════════════════════════════════════════════════════ */

type matchFailure struct {
	hard   bool   // true = required match failed, false = optional
	reason string
}

/*
 * checkMatches — verify every responseMatch against the response body.
 * Marks optional failures on the MatchRule (sets Value to "" as tombstone).
 * Returns nil if all required matches pass.
 */
func checkMatches(body string, rules []MatchRule, params map[string]string) *matchFailure {
	for i := range rules {
		rule := &rules[i]
		value := subst(rule.Value, params)

		if strings.Contains(value, "{{") {
			if rule.Optional {
				rule.Value = "" // tombstone
				continue
			}
			return &matchFailure{hard: true, reason: fmt.Sprintf("unresolved: %s", value)}
		}

		ok := evalMatch(body, value, rule.Type)
		if rule.Invert {
			ok = !ok
		}

		if !ok {
			if rule.Optional {
				rule.Value = "" // tombstone
				continue
			}
			return &matchFailure{hard: true, reason: fmt.Sprintf("no match: %s", value)}
		}
	}
	return nil
}

func evalMatch(body, value, typ string) bool {
	switch typ {
	case "contains":
		return strings.Contains(body, value)
	case "regex":
		re, err := regexp.Compile("(?s)" + value)
		return err == nil && re.MatchString(body)
	}
	return false
}

/*
 * filterFailed — remove tombstoned (failed optional) entries.
 * Keeps matches and redactions aligned by index.
 */
func filterFailed(matches []MatchRule, redactions []RedactionRule) ([]MatchRule, []RedactionRule) {
	var fm []MatchRule
	var fr []RedactionRule

	for i, m := range matches {
		if m.Optional && m.Value == "" {
			continue // tombstoned
		}
		fm = append(fm, m)
		if i < len(redactions) {
			fr = append(fr, redactions[i])
		}
	}
	/* trailing redactions without matches */
	for i := len(matches); i < len(redactions); i++ {
		fr = append(fr, redactions[i])
	}
	return fm, fr
}

/* ═══════════════════════════════════════════════════════════════════
 * BUILD THE TEE PROVE PAYLOAD
 *
 * This is the provider_params_json that goes to /reclaim/prove.
 * Must match the structure in @reclaimprotocol/reclaim-tee exactly.
 * ═══════════════════════════════════════════════════════════════════ */

/* Headers the TEE expects in params.headers (everything else is secret) */
var publicHeaderSet = map[string]bool{
	"user-agent": true, "accept": true, "accept-language": true, "accept-encoding": true,
	"sec-fetch-mode": true, "sec-fetch-site": true, "sec-fetch-user": true, "sec-fetch-dest": true,
	"origin": true, "x-requested-with": true, "sec-ch-ua": true, "sec-ch-ua-mobile": true,
	"sec-ch-ua-platform": true, "content-type": true, "content-length": true,
}

const defaultUA = "Mozilla/5.0 (Linux; Android 13; SM-G991U) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"

func buildProvePayload(req MatchRequest, body string, params map[string]string, matches []MatchRule, redactions []RedactionRule) map[string]interface{} {

	pub, secret := splitParams(params)
	pubH, secH, cookies, referer := splitHeaders(req.Headers)
	addDefaultHeaders(pubH, req.ResponseCT)
	if referer != "" {
		secH["Referer"] = referer
	}
	if req.Geo != "" {
		pub["DYNAMIC_GEO"] = req.Geo
	}

	reqBody := pickBody(req.Pattern.BodySniff, body, req.Method)

	return map[string]interface{}{
		"name": "http",
		"params": map[string]interface{}{
			"url":                req.Pattern.URL,
			"method":             req.Method,
			"body":               reqBody,
			"headers":            pubH,
			"responseMatches":    cleanMatches(matches),
			"responseRedactions": cleanRedactions(redactions),
			"paramValues":        pub,
			"geoLocation":        geoTemplate(req.Geo),
			"writeRedactionMode": "zk",
		},
		"secretParams": map[string]interface{}{
			"headers":     secH,
			"cookieStr":   cookies,
			"paramValues": secret,
		},
	}
}

func splitParams(all map[string]string) (pub, secret map[string]string) {
	pub = make(map[string]string)
	secret = make(map[string]string)
	for k, v := range all {
		if strings.Contains(strings.ToUpper(k), "SECRET") {
			secret[k] = v
		} else {
			pub[k] = v
		}
	}
	return
}

func splitHeaders(raw map[string]string) (pub, secret map[string]string, cookies, referer string) {
	pub = make(map[string]string)
	secret = make(map[string]string)
	for k, v := range raw {
		low := strings.ToLower(k)
		switch {
		case low == "cookie":
			cookies = v
		case low == "referer":
			referer = v
		case publicHeaderSet[low]:
			pub[titleCase(low)] = v
		default:
			secret[titleCase(low)] = v
		}
	}
	return
}

func addDefaultHeaders(h map[string]string, contentType string) {
	if _, ok := h["User-Agent"]; !ok {
		h["User-Agent"] = defaultUA
	}
	if _, ok := h["Accept"]; !ok {
		switch {
		case strings.Contains(contentType, "json"):
			h["Accept"] = "application/json, text/plain, */*"
		case strings.Contains(contentType, "html") || contentType == "":
			h["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
		default:
			h["Accept"] = "*/*"
		}
	}
	setDefault(h, "Accept-Language", "en-US,en;q=0.9")
	setDefault(h, "Sec-Fetch-Dest", "empty")
	setDefault(h, "Sec-Fetch-Mode", "cors")
	setDefault(h, "Sec-Fetch-Site", "same-origin")
}

func pickBody(sniff *BodySniffRule, raw, method string) string {
	if sniff != nil && sniff.Enabled && sniff.Template != "" {
		return sniff.Template
	}
	if strings.ToUpper(method) == "POST" {
		return raw
	}
	return ""
}

func geoTemplate(geo string) string {
	if geo != "" {
		return "{{DYNAMIC_GEO}}"
	}
	return ""
}

func cleanMatches(rules []MatchRule) []map[string]interface{} {
	var out []map[string]interface{}
	for _, m := range rules {
		entry := map[string]interface{}{"value": m.Value, "type": m.Type}
		if m.Invert {
			entry["invert"] = true
		}
		out = append(out, entry)
	}
	return out
}

func cleanRedactions(rules []RedactionRule) []map[string]interface{} {
	var out []map[string]interface{}
	for _, r := range rules {
		entry := make(map[string]interface{})
		if r.XPath != "" {
			entry["xPath"] = r.XPath
		}
		if r.JSONPath != "" {
			entry["jsonPath"] = r.JSONPath
		}
		if r.Regex != "" {
			entry["regex"] = r.Regex
		}
		if r.Hash != "" {
			entry["hash"] = r.Hash
		}
		out = append(out, entry)
	}
	return out
}

/* ═══════════════════════════════════════════════════════════════════
 * TEMPLATE ENGINE
 *
 * Templates look like "hello {{name}}, id={{id}}".
 * convertToRegex turns unknown vars into capture groups.
 * {{varGRD}} = greedy, {{var}} = non-greedy.
 * ═══════════════════════════════════════════════════════════════════ */

var templateVar = regexp.MustCompile(`\{\{(\w+)\}\}`)

func matchTemplate(template, actual string) map[string]string {
	if template == "" || !strings.Contains(template, "{{") {
		return nil
	}
	pattern, vars := convertToRegex(template, nil)
	if len(vars) == 0 {
		return nil
	}
	re, err := regexp.Compile("(?s)" + pattern)
	if err != nil {
		return nil
	}
	m := re.FindStringSubmatch(actual)
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(vars))
	for i, name := range vars {
		if i+1 < len(m) && m[i+1] != "" {
			out[name] = m[i+1]
		}
	}
	return out
}

func convertToRegex(tmpl string, known map[string]string) (string, []string) {
	locs := templateVar.FindAllStringSubmatchIndex(tmpl, -1)
	if len(locs) == 0 {
		return regexp.QuoteMeta(tmpl), nil
	}

	var vars []string
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		name := tmpl[loc[2]:loc[3]]
		b.WriteString(regexp.QuoteMeta(tmpl[prev:loc[0]]))

		if val, ok := known[name]; ok {
			b.WriteString(regexp.QuoteMeta(val))
		} else if strings.HasSuffix(name, "GRD") {
			b.WriteString("(.*)")
			vars = append(vars, name)
		} else {
			b.WriteString("(.*?)")
			vars = append(vars, name)
		}
		prev = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(tmpl[prev:]))
	return b.String(), vars
}

/* ═══════════════════════════════════════════════════════════════════
 * XPATH / JSONPATH / REGEX EVALUATION
 *
 * evalXPath uses the same xpath-go library as the TEE.
 * evalJSONPath walks the parsed JSON tree.
 * evalRegex returns numbered + named capture groups.
 * ═══════════════════════════════════════════════════════════════════ */

var htmlTag = regexp.MustCompile(`<[^>]+>`)

func evalXPath(xpath, html, regex string) string {
	contentsOnly := !(regex != "" && htmlTag.MatchString(regex))

	results, err := xp.QueryWithOptions(xpath, html, xp.Options{
		IncludeLocation: true,
		OutputFormat:    "nodes",
		ContentsOnly:    contentsOnly,
	})
	if err != nil || len(results) == 0 {
		return ""
	}
	return results[0].Value
}

func evalJSONPath(jsonStr, path string) string {
	if !strings.HasPrefix(path, "$") {
		return ""
	}
	var root interface{}
	if json.Unmarshal([]byte(jsonStr), &root) != nil {
		return ""
	}

	cur := root
	for _, seg := range splitPath(strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")) {
		switch node := cur.(type) {
		case map[string]interface{}:
			v, ok := node[seg]
			if !ok {
				return ""
			}
			cur = v
		case []interface{}:
			var idx int
			if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return ""
			}
			cur = node[idx]
		default:
			return ""
		}
	}
	return stringify(cur)
}

func evalRegex(pattern, text string) map[string]string {
	re, err := regexp.Compile("(?s)" + pattern)
	if err != nil {
		return nil
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return nil
	}

	out := make(map[string]string)
	/* numbered groups */
	for i := 1; i < len(m); i++ {
		out[fmt.Sprintf("%d", i)] = m[i]
	}
	/* named groups */
	for _, name := range re.SubexpNames() {
		if name != "" {
			idx := re.SubexpIndex(name)
			if idx > 0 && idx < len(m) && m[idx] != "" {
				out[name] = m[idx]
			}
		}
	}
	return out
}

/* ═══════════════════════════════════════════════════════════════════
 * SMALL HELPERS
 *
 * These are intentionally trivial. No cleverness.
 * ═══════════════════════════════════════════════════════════════════ */

func subst(tmpl string, params map[string]string) string {
	s := tmpl
	for k, v := range params {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func bodyToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func varAt(names []string, i int) string {
	if i < len(names) {
		return names[i]
	}
	return ""
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func setDefault(m map[string]string, key, val string) {
	if _, ok := m[key]; !ok {
		m[key] = val
	}
}

func titleCase(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

func stringify(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int(val)) {
			return fmt.Sprintf("%d", int(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	var segs []string
	var buf strings.Builder
	bracket := false
	for _, r := range path {
		switch {
		case r == '.' && !bracket:
			if buf.Len() > 0 {
				segs = append(segs, buf.String())
				buf.Reset()
			}
		case r == '[':
			if buf.Len() > 0 {
				segs = append(segs, buf.String())
				buf.Reset()
			}
			bracket = true
		case r == ']' && bracket:
			segs = append(segs, strings.Trim(buf.String(), "'\""))
			buf.Reset()
			bracket = false
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		segs = append(segs, buf.String())
	}
	return segs
}
