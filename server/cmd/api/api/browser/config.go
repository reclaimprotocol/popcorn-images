package browser

import "encoding/json"

// SessionConfig is the start-session payload (the core subset of the portal's
// SessionCreateRequestBody). Provider-selection fields are intentionally absent
// — there is exactly one provider now (the local Chromium).
type SessionConfig struct {
	// SessionID is optional; a uuid is generated when empty.
	SessionID string
	// AppID / ProviderID identify the provider config for correlation/logging.
	AppID      string
	ProviderID string
	// ProviderConfig is required (carries loginUrl/viewport/etc.).
	ProviderConfig *ProviderConfig
	// Parameters are session-level parameters passed through from the gateway.
	Parameters map[string]string
}

// ProviderConfig holds only the fields the foundation reads. Launch/claim/AI
// fields are intentionally omitted; Extra preserves any other keys received on
// the wire so nothing is lost (the portal ProviderConfig has an open index
// signature).
type ProviderConfig struct {
	ProviderID      string    `json:"providerId"`
	AppID           string    `json:"appId,omitempty"`
	LoginURL        string    `json:"loginUrl"`
	UserAgent       string    `json:"userAgent,omitempty"`
	Viewport        *Viewport `json:"viewport,omitempty"`
	InjectionType   string    `json:"injectionType,omitempty"`   // HAWKEYE|NONE|CDP — read in Phase 5
	CustomInjection string    `json:"customInjection,omitempty"` // Phase 5
	LogLevel        string    `json:"logLevel,omitempty"`
	GeoLocation     string    `json:"geoLocation,omitempty"` // provider-level geo (proxy region for the proof fetch)

	// RequestData lists the requests to match + prove. Empty means no proof
	// attempts (capture-only) — the default, so existing flows are unaffected.
	RequestData []RequestMatcher `json:"requestData,omitempty"`

	// Extra preserves all other provider-config fields not modeled above.
	Extra map[string]any `json:"-"`
}

// Viewport is the browser viewport size.
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// RequestMatcher describes a request to capture and prove. JSON tags match the
// portal wire shape so it can be decoded directly from provider_config.
// responseMatches/responseRedactions are kept as raw JSON and passed through to
// reclaim-tee verbatim, so fields like invert/isOptional/order/hash are never
// dropped.
type RequestMatcher struct {
	URL                string          `json:"url"`
	URLType            string          `json:"urlType,omitempty"` // EXACT|CONSTANT|REGEX|TEMPLATE
	Method             string          `json:"method,omitempty"`
	ResponseMatches    json.RawMessage `json:"responseMatches,omitempty"`
	ResponseRedactions json.RawMessage `json:"responseRedactions,omitempty"`
	// ResponseVariables names the variables (in redaction order) for redactions
	// whose regex has no named group — used to key extracted paramValues.
	ResponseVariables  []string   `json:"responseVariables,omitempty"`
	GeoLocation        string     `json:"geoLocation,omitempty"`
	WriteRedactionMode string     `json:"writeRedactionMode,omitempty"` // zk|keyUpdate
	Context            string     `json:"context,omitempty"`            // provider-supplied context string
	BodySniff          *BodySniff `json:"bodySniff,omitempty"`
}

// BodySniff lets a matcher supply a request-body template for the claim.
type BodySniff struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Template string `json:"template,omitempty"`
}
