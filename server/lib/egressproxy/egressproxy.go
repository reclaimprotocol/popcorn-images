// Package egressproxy parses the shared HTTPS_PROXY_URL template and composes
// the per-session proxy username, mirroring reclaim-tee's client/proxy.go so
// the browser egress and the TEE proof's target fetch go through the EXACT same
// proxy + sticky session (one exit IP for both).
//
// reclaim-tee (shared.GetHTTPSProxyURL → client/proxy.go) does:
//   - require the template to contain "{{geoLocation}}"
//   - substitute {{geoLocation}} with the lowercased ISO-2 country code
//   - append "-session-<requestId>" to the proxy username when requestId is set
//   - dial the target via HTTP CONNECT through the resulting proxy
//
// We reproduce that substitution + username composition exactly so the browser
// presents the identical credentials (same -country-<geo>-session-<sessionId>),
// landing on the same BrightData exit IP the attestor witnesses from.
package egressproxy

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const geoPlaceholder = "{{geoLocation}}"

// Proxy is a parsed egress proxy ready to apply to a browser session.
type Proxy struct {
	Scheme   string // "http" / "https" (how to reach the proxy server)
	Host     string
	Port     int
	Username string // template username with {{geoLocation}} already substituted, BEFORE the -session suffix
	Password string
}

// Parse resolves the HTTPS_PROXY_URL template for a given geoLocation (ISO-2
// country, e.g. "US"; empty falls back to the caller's default). It returns
// (nil, nil) when template is empty — i.e. proxying disabled — so callers can
// treat "no proxy configured" and "geo missing" uniformly.
func Parse(template, geoLocation string) (*Proxy, error) {
	if strings.TrimSpace(template) == "" {
		return nil, nil
	}
	if !strings.Contains(template, geoPlaceholder) {
		return nil, fmt.Errorf("HTTPS_PROXY_URL must contain the %s placeholder", geoPlaceholder)
	}
	// Mirror reclaim-tee: lowercased substitution. The caller is responsible for
	// supplying a valid ISO-2 code (reclaim-tee validates it on its side).
	resolved := strings.ReplaceAll(template, geoPlaceholder, strings.ToLower(geoLocation))

	u, err := url.Parse(resolved)
	if err != nil {
		return nil, fmt.Errorf("parse HTTPS_PROXY_URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("HTTPS_PROXY_URL has no host")
	}

	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	host := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		// Default proxy ports by scheme; BrightData uses an explicit port so this
		// is just a safety net.
		if scheme == "https" {
			portStr = "443"
		} else {
			portStr = "80"
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("HTTPS_PROXY_URL invalid port %q: %w", portStr, err)
	}

	p := &Proxy{Scheme: scheme, Host: host, Port: port}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}

// NormalizeCountry returns geo as a canonical upper-case ISO 3166-1 alpha-2
// code when it looks like one (exactly two ASCII letters), otherwise fallback
// (also upper-cased). This guards against values that aren't real countries —
// empty, or unresolved provider placeholders like "{{DYNAMIC_GEO}}" — which
// would otherwise corrupt the proxy username (invalid URL userinfo, breaking
// Parse) and be rejected by reclaim-tee's validateGeoLocation. It is NOT a full
// ISO validator (a 2-letter non-country like "ZZ" passes here and would be
// caught downstream); it only filters the broken/placeholder cases we see.
func NormalizeCountry(geo, fallback string) string {
	g := strings.TrimSpace(geo)
	if len(g) == 2 && isASCIILetter(g[0]) && isASCIILetter(g[1]) {
		return strings.ToUpper(g)
	}
	return strings.ToUpper(strings.TrimSpace(fallback))
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// SessionUsername appends the BrightData sticky-session suffix exactly as
// reclaim-tee does (username + "-session-" + sessionID). With an empty
// sessionID it returns the base username unchanged. This is what the browser's
// CDP Fetch.authRequired handler must answer with so the browser shares the
// attestor's exit IP.
func (p *Proxy) SessionUsername(sessionID string) string {
	if p == nil {
		return ""
	}
	if sessionID == "" {
		return p.Username
	}
	return p.Username + "-session-" + sessionID
}
