package config

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config holds all configuration for the server
type Config struct {
	// Server configuration
	Port int `envconfig:"PORT" default:"10001"`

	// Recording configuration
	FrameRate   int    `envconfig:"FRAME_RATE" default:"10"`
	DisplayNum  int    `envconfig:"DISPLAY_NUM" default:"1"`
	MaxSizeInMB int    `envconfig:"MAX_SIZE_MB" default:"500"`
	OutputDir   string `envconfig:"OUTPUT_DIR" default:"."`

	// HideCursorDefault hides the mouse cursor automatically — at server boot
	// (session-independent, covers the neko live view) and again on each
	// /session/start. Defaults to true; set HIDE_CURSOR_DEFAULT=false to keep
	// the cursor visible until /computer/cursor is called explicitly.
	HideCursorDefault bool `envconfig:"HIDE_CURSOR_DEFAULT" default:"true"`

	// Absolute or relative path to the ffmpeg binary. If empty the code falls back to "ffmpeg" on $PATH.
	PathToFFmpeg string `envconfig:"FFMPEG_PATH" default:"ffmpeg"`

	// DevTools proxy configuration
	DevToolsProxyPort int  `envconfig:"DEVTOOLS_PROXY_PORT" default:"9222"`
	LogCDPMessages    bool `envconfig:"LOG_CDP_MESSAGES" default:"false"`

	// ChromeDriver proxy: external port where the proxy listens.
	ChromeDriverProxyPort int `envconfig:"CHROMEDRIVER_PROXY_PORT" default:"9224"`
	// Internal ChromeDriver upstream used by the ChromeDriver proxy.
	ChromeDriverUpstreamAddr string `envconfig:"CHROMEDRIVER_UPSTREAM_ADDR" default:"127.0.0.1:9225"`
	// DevTools proxy address passed to ChromeDriver as goog:chromeOptions.debuggerAddress.
	// If empty, it is derived from DevToolsProxyPort as 127.0.0.1:<port>.
	DevToolsProxyAddr string `envconfig:"DEVTOOLS_PROXY_ADDR" default:""`

	// Internal CDP proxy (port 9226) - unrestricted, full CDP access for internal services
	// Note: Port 9222 is restricted CDP (filtered), port 9224 is WebDriver/BiDi, port 9226 is internal/full CDP

	// Reclaim TEE configuration
	TEEKUrl     string `envconfig:"TEE_K_URL" default:"wss://tk.reclaimprotocol.org/ws"`
	TEETUrl     string `envconfig:"TEE_T_URL" default:"wss://tt.reclaimprotocol.org/ws"`
	AttestorUrl string `envconfig:"ATTESTOR_URL" default:"wss://attestor.reclaimprotocol.org:444/ws"`

	// Reclaim backend (api.reclaimprotocol.org): session bootstrap, status
	// updates, feature flags, and proof callback submission. Auth is via the
	// X-Reclaim-Session-Id header only (no app secret in the image).
	ReclaimBackendURL string `envconfig:"RECLAIM_BACKEND_URL" default:"https://api.reclaimprotocol.org"`

	// HTTPSProxyURL is the egress-proxy URL *template* shared by the TEE proof
	// and the browser. reclaim-tee reads the same HTTPS_PROXY_URL env directly
	// (shared.GetHTTPSProxyURL) and dials the target through it; we also parse
	// it here so the browser session can egress through the SAME proxy + sticky
	// session, giving both halves one exit IP. Must contain the {{geoLocation}}
	// placeholder, e.g.
	//   http://brd-customer-XXX-zone-YYY-country-{{geoLocation}}:PASS@brd.superproxy.io:33335
	// reclaim-tee substitutes {{geoLocation}} (lowercase ISO-2) and appends
	// "-session-<requestId>" to the username. Empty disables proxying.
	HTTPSProxyURL string `envconfig:"HTTPS_PROXY_URL" default:""`

	// TEEProofTimeout caps a single reclaim-tee proof attempt. reclaim-tee's own
	// "core TEE protocol" timeout is hardcoded at 60s and can stall there (e.g.
	// a proxied fetch that captures the response but never finalizes), so we
	// enforce a shorter deadline here: when it elapses, the attempt is abandoned
	// and a FAILED claim (with the assembled claim request + error) is returned
	// immediately, instead of blocking the session for the full 60s. Default 30s.
	TEEProofTimeout time.Duration `envconfig:"TEE_PROOF_TIMEOUT" default:"30s"`

	// ProxyDefaultCountry is an OPTIONAL ISO-3166-1 alpha-2 fallback for when a
	// provider's geoLocation isn't a usable country code — empty, or an
	// unresolved placeholder like "{{DYNAMIC_GEO}}". Default is empty, which
	// means SKIP proxying in that case (browser + TEE egress direct) rather than
	// guessing a country. Set it (e.g. "IN") to force a fallback country instead.
	ProxyDefaultCountry string `envconfig:"PROXY_DEFAULT_COUNTRY" default:""`
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	var config Config
	if err := envconfig.Process("", &config); err != nil {
		return nil, err
	}
	if config.DevToolsProxyAddr == "" {
		config.DevToolsProxyAddr = fmt.Sprintf("127.0.0.1:%d", config.DevToolsProxyPort)
	}
	if err := validate(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

func validate(config *Config) error {
	if config.OutputDir == "" {
		return fmt.Errorf("OUTPUT_DIR is required")
	}
	if config.DisplayNum < 0 {
		return fmt.Errorf("DISPLAY_NUM must be greater than 0")
	}
	if config.FrameRate < 0 || config.FrameRate > 20 {
		return fmt.Errorf("FRAME_RATE must be greater than 0 and less than or equal to 20")
	}
	if config.MaxSizeInMB < 0 || config.MaxSizeInMB > 1000 {
		return fmt.Errorf("MAX_SIZE_MB must be greater than 0 and less than or equal to 1000")
	}
	if config.PathToFFmpeg == "" {
		return fmt.Errorf("FFMPEG_PATH is required")
	}
	if config.ChromeDriverUpstreamAddr == "" {
		return fmt.Errorf("CHROMEDRIVER_UPSTREAM_ADDR is required")
	}
	if config.DevToolsProxyAddr == "" {
		return fmt.Errorf("DEVTOOLS_PROXY_ADDR is required")
	}

	return nil
}
