package browser

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
type RequestMatcher struct {
	URL                string              `json:"url"`
	URLType            string              `json:"urlType,omitempty"` // EXACT|CONSTANT|REGEX|TEMPLATE
	Method             string              `json:"method,omitempty"`
	ResponseMatches    []ResponseMatch     `json:"responseMatches,omitempty"`
	ResponseRedactions []ResponseRedaction `json:"responseRedactions,omitempty"`
	GeoLocation        string              `json:"geoLocation,omitempty"`
}

// ResponseMatch is a reclaim-tee response match (Value/Type).
type ResponseMatch struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"` // contains|regex
}

// ResponseRedaction is a reclaim-tee response redaction.
type ResponseRedaction struct {
	XPath    string `json:"xPath,omitempty"`
	JSONPath string `json:"jsonPath,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Hash     string `json:"hash,omitempty"`
}
