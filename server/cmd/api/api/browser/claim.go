package browser

import (
	"encoding/json"
	"fmt"
)

// ClaimResult is the sanitized proof result surfaced on the event stream and
// GET /session/claim. It carries no secrets.
type ClaimResult struct {
	Identifier      string `json:"identifier,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Parameters      string `json:"parameters,omitempty"`
	Owner           string `json:"owner,omitempty"`
	Context         string `json:"context,omitempty"`
	Epoch           int    `json:"epoch,omitempty"`
	TimestampS      int    `json:"timestamp_s,omitempty"`
	AttestorAddress string `json:"attestor_address,omitempty"`
	ClaimSignature  string `json:"claim_signature,omitempty"` // base64
	Error           string `json:"error,omitempty"`
}

// capturedForProof is the minimal captured request/response data needed to
// assemble a proof. Secrets (cookie) stay in-process and never hit the stream.
type capturedForProof struct {
	URL    string
	Method string
	Cookie string
	Body   string
}

// buildProviderParamsJSON assembles the reclaim-tee provider_params_json for a
// matched request, following the portal worker's shape:
//
//	{ name:"http", params:{url, method, responseMatches, responseRedactions,
//	  paramValues, [geoLocation]}, secretParams:{[headers.cookie], [paramValues]} }
//
// NOTE: this shape must be validated end-to-end against a real provider config
// and a live TEE before it can be relied on (Phase-4 acceptance gate). It is
// only exercised when the gateway sends provider_config.requestData.
func buildProviderParamsJSON(m RequestMatcher, cap capturedForProof) (string, error) {
	matches, err := sanitizeResponseMatches(m.ResponseMatches)
	if err != nil {
		return "", err
	}
	redactions, err := sanitizeResponseRedactions(m.ResponseRedactions)
	if err != nil {
		return "", err
	}

	allParams := extractURLParams(m.URL, cap.URL)
	publicParams, secretParams := separateSecrets(allParams)

	params := map[string]any{
		"url":         cap.URL,
		"method":      cap.Method,
		"paramValues": publicParams,
	}
	// reclaim-tee validates params against a strict schema (additionalProperties
	// false): only {value,type[,invert]} on responseMatches and
	// {xPath,jsonPath,regex[,hash]} on responseRedactions. Project to that shape —
	// drop order/description/isOptional and omit empty/null hash.
	if len(matches) > 0 {
		params["responseMatches"] = matches
	}
	if len(redactions) > 0 {
		params["responseRedactions"] = redactions
	}
	if m.GeoLocation != "" {
		params["geoLocation"] = m.GeoLocation
	}

	secret := map[string]any{}
	if cap.Cookie != "" {
		secret["headers"] = map[string]string{"cookie": cap.Cookie}
	}
	if len(secretParams) > 0 {
		secret["paramValues"] = secretParams
	}

	out := map[string]any{
		"name":   "http",
		"params": params,
	}
	if len(secret) > 0 {
		out["secretParams"] = secret
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// teeResponseMatch is the exact shape reclaim-tee accepts for a response match.
type teeResponseMatch struct {
	Value  string `json:"value"`
	Type   string `json:"type,omitempty"`
	Invert bool   `json:"invert,omitempty"`
}

// teeResponseRedaction is the exact shape reclaim-tee accepts for a redaction.
// hash is omitted unless it's a valid non-empty mode (e.g. "oprf"/"oprf-mpc").
type teeResponseRedaction struct {
	XPath    string `json:"xPath"`
	JSONPath string `json:"jsonPath"`
	Regex    string `json:"regex"`
	Hash     string `json:"hash,omitempty"`
}

// sanitizeResponseMatches projects portal responseMatches down to reclaim-tee's
// allowed fields (dropping description/order/isOptional, which the schema rejects).
func sanitizeResponseMatches(raw json.RawMessage) ([]teeResponseMatch, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in []struct {
		Value  string `json:"value"`
		Type   string `json:"type"`
		Invert bool   `json:"invert"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("responseMatches: %w", err)
	}
	out := make([]teeResponseMatch, 0, len(in))
	for _, m := range in {
		out = append(out, teeResponseMatch{Value: m.Value, Type: m.Type, Invert: m.Invert})
	}
	return out, nil
}

// sanitizeResponseRedactions projects portal responseRedactions down to
// reclaim-tee's allowed fields (dropping order) and omits empty/null hash.
// "oprf-raw" is rejected; "oprf"/"oprf-mpc" pass through.
func sanitizeResponseRedactions(raw json.RawMessage) ([]teeResponseRedaction, error) {
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
	out := make([]teeResponseRedaction, 0, len(in))
	for _, r := range in {
		red := teeResponseRedaction{XPath: r.XPath, JSONPath: r.JSONPath, Regex: r.Regex}
		if r.Hash != nil && *r.Hash != "" {
			if *r.Hash == "oprf-raw" {
				return nil, fmt.Errorf("oprf-raw responseRedaction is not supported in-image")
			}
			red.Hash = *r.Hash
		}
		out = append(out, red)
	}
	return out, nil
}
