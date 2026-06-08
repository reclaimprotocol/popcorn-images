package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/onkernel/kernel-images/server/cmd/api/api/browser"
	"github.com/reclaimprotocol/reclaim-tee/client"
	"github.com/reclaimprotocol/reclaim-tee/providers"
)

// browserProver is the browser.Prover injected into the session manager. For a
// matched captured request it extracts the response variables, assembles the
// reclaim-tee provider_params_json (the exact {name,params,secretParams,context}
// shape reclaim-tee accepts), runs the proof, and maps the result.
func (s *ApiService) browserProver(ctx context.Context, m browser.RequestMatcher, captured browser.CapturedForProof, requestID string) (*browser.ClaimResult, error) {
	ppj, templateContext, paramValues, fullClaim, err := s.buildBrowserProviderParams(m, captured, requestID)
	if err != nil {
		return nil, err
	}
	cfgJSON, err := s.buildReclaimClientConfigJSON(requestID)
	if err != nil {
		return nil, err
	}

	// publicData is attached to both success and failure results.
	var publicData json.RawMessage
	if captured.PublicData != "" && json.Valid([]byte(captured.PublicData)) {
		publicData = json.RawMessage(captured.PublicData)
	}

	claim, err := s.executeReclaimProve(ctx, ppj, cfgJSON)
	if err != nil {
		// Surface the COMPLETE assembled claim request (full params incl.
		// proxySessionId/headers + secretParams with cookie + context) alongside
		// the error, so the caller can retry (e.g. via the client-side attestor).
		fullClaimBytes, _ := json.Marshal(fullClaim)
		paramsJSON := ""
		if pb, e := json.Marshal(fullClaim["params"]); e == nil {
			paramsJSON = string(pb)
		}
		failed := &browser.ClaimResult{
			ClaimData:    &browser.ClaimData{Parameters: paramsJSON},
			PublicData:   publicData,
			ClaimRequest: json.RawMessage(fullClaimBytes),
			Error:        err.Error(),
		}
		if len(paramValues) > 0 {
			failed.ExtractedParameterValues = paramValues
		}
		return failed, err
	}

	res := s.transformClaim(claim, templateContext)
	// Top-level convenience fields (parsed), matching the portal proof shape.
	if len(paramValues) > 0 {
		res.ExtractedParameterValues = paramValues
	}
	res.PublicData = publicData
	return res, nil
}

// teeResponseMatch / teeResponseRedaction are the exact shapes reclaim-tee's
// schema accepts (additionalProperties:false). Portal extras (description,
// isOptional, order) are dropped; hash is omitted unless it's a valid mode.
type teeResponseMatch struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type teeResponseRedaction struct {
	XPath    string `json:"xPath"`
	JSONPath string `json:"jsonPath"`
	Regex    string `json:"regex"`
	Hash     string `json:"hash,omitempty"`
}

type parsedRedaction struct {
	XPath    string
	JSONPath string
	Regex    string
	Hash     string // sanitized: "" or "oprf"/"oprf-mpc"/...
}

// buildBrowserProviderParams assembles provider_params_json for a matched
// request, extracting paramValues from the captured body so responseMatches
// templates ({{var}}) resolve (an empty paramValues makes reclaim-tee panic).
func (s *ApiService) buildBrowserProviderParams(m browser.RequestMatcher, captured browser.CapturedForProof, requestID string) (ppj string, templateContext string, publicParamValues map[string]string, fullClaim map[string]any, err error) {
	matches, err := sanitizeMatches(m.ResponseMatches)
	if err != nil {
		return "", "", nil, nil, err
	}
	reds, err := parseRedactions(m.ResponseRedactions)
	if err != nil {
		return "", "", nil, nil, err
	}

	// Extract each response variable from the captured body into paramValues,
	// then split into public/secret.
	allParams := map[string]string{}
	for i, rr := range reds {
		fallbackName := ""
		if i < len(m.ResponseVariables) {
			fallbackName = m.ResponseVariables[i]
		}
		s.extractRedactionParams(captured.Body, rr, fallbackName, allParams)
	}
	publicParamValues, secretParamValues := splitParamValues(allParams)

	if matches == nil {
		matches = []teeResponseMatch{}
	}
	teeReds := make([]teeResponseRedaction, 0, len(reds))
	for _, rr := range reds {
		teeReds = append(teeReds, teeResponseRedaction{
			XPath: rr.XPath, JSONPath: rr.JSONPath, Regex: rr.Regex, Hash: rr.Hash,
		})
	}

	publicHdrs, secretHdrs := separateRequestHeaders(captured.Headers)

	claimURL := m.URL
	if claimURL == "" {
		claimURL = captured.URL
	}
	method := captured.Method
	if method == "" {
		method = "GET"
	}
	body := ""
	switch {
	case m.BodySniff != nil && m.BodySniff.Enabled && m.BodySniff.Template != "":
		body = m.BodySniff.Template
	case strings.EqualFold(method, "POST") && captured.RequestBody != "":
		body = captured.RequestBody
	}
	geo := m.GeoLocation
	if geo == "" {
		geo = "IN" // portal default; override via provider_config.geoLocation
	}

	// fullParams is the complete (portal buildClaimObject) param set, including
	// proxySessionId / public headers / writeRedactionMode. This is kept for the
	// claimRequest returned on failure (e.g. client-side attestor retry).
	fullParams := map[string]any{
		"geoLocation":             geo,
		"proxySessionId":          requestID,
		"url":                     claimURL,
		"method":                  method,
		"body":                    body,
		"headers":                 publicHdrs,
		"responseRedactions":      teeReds,
		"responseMatches":         matches,
		"paramValues":             publicParamValues,
		"additionalClientOptions": map[string]any{},
	}
	if m.WriteRedactionMode != "" {
		fullParams["writeRedactionMode"] = m.WriteRedactionMode
	}

	// Secret headers + cookie (folded into headers.cookie, as the portal does at
	// prove time) + any secret param values.
	if captured.Cookie != "" {
		secretHdrs["cookie"] = captured.Cookie
	}
	secretParams := map[string]any{"headers": secretHdrs}
	if len(secretParamValues) > 0 {
		secretParams["paramValues"] = secretParamValues
	}

	// Context = provider/matcher-supplied context (matchingRequestData.context),
	// defaulting to "{}". publicData is NOT placed here — it's surfaced as a
	// top-level field on the proof (transformClaim merges this context with the
	// TEE-generated context).
	ctxStr := m.Context
	if ctxStr == "" {
		ctxStr = "{}"
	}

	fullClaim = map[string]any{
		"name":         "http",
		"params":       fullParams,
		"secretParams": secretParams,
		"context":      ctxStr,
	}

	// The TEE schema rejects proxySessionId in params (the portal strips it
	// before proving), so send a copy of params without it.
	teeParams := make(map[string]any, len(fullParams))
	for k, v := range fullParams {
		teeParams[k] = v
	}
	delete(teeParams, "proxySessionId")

	teeOut := map[string]any{
		"name":         "http",
		"params":       teeParams,
		"secretParams": secretParams,
		"context":      ctxStr,
	}
	b, err := json.Marshal(teeOut)
	if err != nil {
		return "", "", nil, nil, err
	}
	return string(b), ctxStr, publicParamValues, fullClaim, nil
}

// transformClaim maps a reclaim-tee result into the portal proof shape
// (transformProof): {identifier, claimData{...}, witnesses, signatures:["0x"+hex]},
// merging the template context (which carries publicData) into the TEE context.
func (s *ApiService) transformClaim(c *client.ClaimWithSignatures, templateContext string) *browser.ClaimResult {
	res := &browser.ClaimResult{ClaimData: &browser.ClaimData{}}

	type providerClaimData interface {
		GetProvider() string
		GetParameters() string
		GetOwner() string
		GetTimestampS() uint32
		GetContext() string
		GetIdentifier() string
		GetEpoch() uint32
	}
	if cd, ok := any(c.Claim).(providerClaimData); ok {
		res.ClaimData.Provider = cd.GetProvider()
		res.ClaimData.Parameters = cd.GetParameters()
		res.ClaimData.Owner = cd.GetOwner()
		res.ClaimData.TimestampS = int(cd.GetTimestampS())
		res.ClaimData.Identifier = cd.GetIdentifier()
		res.ClaimData.Epoch = int(cd.GetEpoch())
		res.ClaimData.Context = mergeContext(templateContext, cd.GetContext())
		res.Identifier = cd.GetIdentifier()
	}

	type claimSignature interface {
		GetAttestorAddress() string
		GetClaimSignature() []byte
	}
	if sg, ok := any(c.Signature).(claimSignature); ok {
		res.Witnesses = []browser.Witness{{ID: sg.GetAttestorAddress(), URL: s.config.AttestorUrl}}
		res.Signatures = []string{"0x" + hex.EncodeToString(sg.GetClaimSignature())}
	}
	return res
}

// mergeContext returns {...template, ...tee} — TEE fields win, but template-only
// fields (e.g. publicData) are preserved. Falls back to the TEE context if
// either side doesn't parse.
func mergeContext(template, tee string) string {
	var t, e map[string]any
	if json.Unmarshal([]byte(template), &t) != nil {
		t = map[string]any{}
	}
	if json.Unmarshal([]byte(tee), &e) != nil {
		return tee
	}
	merged := map[string]any{}
	for k, v := range t {
		merged[k] = v
	}
	for k, v := range e {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return tee
	}
	return string(b)
}

// extractRedactionParams extracts the value a redaction selects from body and
// records it in out. It narrows by xPath then jsonPath, then applies the regex
// best-effort: a named group keys by its name; an unnamed group yields capture
// group 1; if the regex doesn't match the narrowed slice, the (json-normalized)
// slice itself is used. The paramValues key is responseVariables[i]
// (fallbackName) when present, else the regex's named-group name — because
// reclaim-tee resolves responseMatches {{var}} against these keys.
func (s *ApiService) extractRedactionParams(body string, rr parsedRedaction, fallbackName string, out map[string]string) {
	element := body
	hadJSON := false

	if rr.XPath != "" {
		ranges, err := providers.ExtractHTMLElementsIndexes(element, rr.XPath, rr.JSONPath != "")
		if err != nil || len(ranges) == 0 {
			return
		}
		element = element[ranges[0].Start:ranges[0].End]
	}
	if rr.JSONPath != "" {
		ranges, err := providers.ExtractJSONValueIndexes([]byte(element), rr.JSONPath)
		if err != nil || len(ranges) == 0 {
			return
		}
		element = element[ranges[0].Start:ranges[0].End]
		hadJSON = true
	}

	value := element
	namedName := ""
	if rr.Regex != "" {
		re, err := makeTEERegex(rr.Regex)
		if err != nil {
			return
		}
		if m := re.FindStringSubmatch(element); m != nil {
			// Prefer a named group; else capture group 1; else the whole match.
			picked := ""
			for i, n := range re.SubexpNames() {
				if i > 0 && n != "" && i < len(m) {
					picked = m[i]
					namedName = n
					break
				}
			}
			if picked == "" && len(m) > 1 {
				picked = m[1]
			}
			if picked == "" {
				picked = m[0]
			}
			value = picked
		} else if hadJSON {
			// Regex didn't match the narrowed value (common when jsonPath already
			// isolated it) — use the json-normalized value.
			value = normalizeJSONValue(element)
		}
	} else if hadJSON {
		value = normalizeJSONValue(element)
	}
	value = strings.TrimSpace(value)

	// Record under both keys (matching the portal): the regex named-group name
	// and the responseVariables[i] name. reclaim-tee resolves {{var}} against
	// whichever the responseMatch uses.
	if namedName != "" {
		out[namedName] = value
	}
	if fallbackName != "" {
		out[fallbackName] = value
	}
}

// publicHeaderSet is the portal's DEFAULT_PUBLIC_HEADERS — request headers that
// are sent as public (params.headers). Everything else is secret.
var publicHeaderSet = map[string]bool{
	"user-agent":       true,
	"accept":           true,
	"accept-language":  true,
	"accept-encoding":  true,
	"sec-fetch-mode":   true,
	"sec-fetch-site":   true,
	"sec-fetch-user":   true,
	"origin":           true,
	"x-requested-with": true,
	"sec-ch-ua":        true,
	"sec-ch-ua-mobile": true,
}

// separateRequestHeaders splits the captured request headers into public
// (Title-Cased, from publicHeaderSet) and secret (everything else). Cookie is
// excluded here — it's carried via cookieStr/secretParams.headers.cookie. No
// synthetic defaults are added; only what the real request sent.
func separateRequestHeaders(req map[string]string) (public, secret map[string]string) {
	public = map[string]string{}
	secret = map[string]string{}
	for k, v := range req {
		lk := strings.ToLower(k)
		if lk == "cookie" {
			continue
		}
		if publicHeaderSet[lk] {
			public[toTitleCaseHeader(k)] = v
		} else {
			secret[k] = v
		}
	}
	return public, secret
}

func toTitleCaseHeader(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, "-")
}

// splitParamValues separates extracted params into public and secret by the
// portal's rule (key contains "SECRET", case-insensitive).
func splitParamValues(all map[string]string) (public, secret map[string]string) {
	public = map[string]string{}
	secret = map[string]string{}
	for k, v := range all {
		if strings.Contains(strings.ToUpper(k), "SECRET") {
			secret[k] = v
		} else {
			public[k] = v
		}
	}
	return public, secret
}

func sanitizeMatches(raw json.RawMessage) ([]teeResponseMatch, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in []struct {
		Value string `json:"value"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("responseMatches: %w", err)
	}
	out := make([]teeResponseMatch, 0, len(in))
	for _, mm := range in {
		out = append(out, teeResponseMatch{Value: mm.Value, Type: mm.Type})
	}
	return out, nil
}

func parseRedactions(raw json.RawMessage) ([]parsedRedaction, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in []struct {
		XPath    string  `json:"xPath"`
		JSONPath string  `json:"jsonPath"`
		Regex    string  `json:"regex"`
		Hash     *string `json:"hash"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("responseRedactions: %w", err)
	}
	out := make([]parsedRedaction, 0, len(in))
	for _, r := range in {
		pr := parsedRedaction{XPath: r.XPath, JSONPath: r.JSONPath, Regex: r.Regex}
		if r.Hash != nil && *r.Hash != "" {
			if *r.Hash == "oprf-raw" {
				return nil, fmt.Errorf("oprf-raw responseRedaction is not supported in-image")
			}
			pr.Hash = *r.Hash
		}
		out = append(out, pr)
	}
	return out, nil
}
