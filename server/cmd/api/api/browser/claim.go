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
	for _, rr := range m.ResponseRedactions {
		if rr.Hash == "oprf-raw" {
			return "", fmt.Errorf("oprf-raw responseRedaction is not supported in-image")
		}
	}

	allParams := extractURLParams(m.URL, cap.URL)
	publicParams, secretParams := separateSecrets(allParams)

	params := map[string]any{
		"url":                cap.URL,
		"method":             cap.Method,
		"responseMatches":    m.ResponseMatches,
		"responseRedactions": m.ResponseRedactions,
		"paramValues":        publicParams,
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
