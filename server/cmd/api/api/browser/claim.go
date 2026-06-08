package browser

import "encoding/json"

// ClaimResult is the transformed proof surfaced on the event stream and
// GET /session/claim, matching the portal's transformProof output shape.
type ClaimResult struct {
	Identifier               string            `json:"identifier,omitempty"`
	ClaimData                *ClaimData        `json:"claimData,omitempty"`
	Signatures               []string          `json:"signatures,omitempty"`
	Witnesses                []Witness         `json:"witnesses,omitempty"`
	ExtractedParameterValues map[string]string `json:"extractedParameterValues,omitempty"`
	PublicData               json.RawMessage   `json:"publicData,omitempty"`
	// ClaimRequest is the complete assembled provider_params object that was
	// submitted to the TEE — including filled secretParams (cookie, secret
	// headers, secret paramValues). Populated only on failure so the caller can
	// retry the proof (e.g. via the client-side attestor). Contains secrets.
	ClaimRequest json.RawMessage `json:"claimRequest,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// ClaimData mirrors the portal claimData object (note camelCase timestampS).
type ClaimData struct {
	Provider   string `json:"provider,omitempty"`
	Parameters string `json:"parameters,omitempty"`
	Owner      string `json:"owner,omitempty"`
	TimestampS int    `json:"timestampS,omitempty"`
	Context    string `json:"context,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	Epoch      int    `json:"epoch,omitempty"`
}

// Witness identifies the attestor that signed the claim.
type Witness struct {
	ID  string `json:"id,omitempty"`
	URL string `json:"url,omitempty"`
}

// CapturedForProof is the captured request/response data handed to the Prover.
// The Prover (in package api) extracts response-variable values and assembles
// the reclaim-tee provider_params_json. Secrets (cookie) stay in-process.
type CapturedForProof struct {
	URL    string
	Method string
	Cookie string
	Body   string // response body
	// RequestBody is the request post data (for POST/PUT); "" for GET.
	RequestBody string
	// Headers are the captured request headers (a public subset is sent to the
	// TEE; secrets like cookie are routed via secretParams).
	Headers map[string]string
	// PublicData is the latest window.Reclaim.updatePublicData payload (raw
	// JSON), merged into the proof context. Empty when none was set.
	PublicData string
}
