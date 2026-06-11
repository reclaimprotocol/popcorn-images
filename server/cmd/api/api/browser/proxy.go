package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onkernel/kernel-images/server/lib/cdpclient"
)

// ProxyConfig is a fully-resolved egress proxy for one session: host:port to
// route through, plus the per-session credentials. Username already carries the
// "-country-<geo>-session-<sessionId>" suffix so the browser lands on the SAME
// BrightData exit IP the TEE proof witnesses from (see lib/egressproxy +
// reclaim-tee client/proxy.go).
type ProxyConfig struct {
	Scheme   string
	Host     string
	Port     int
	Username string
	Password string
}

// ProxyResolver returns the proxy to apply for a session, or nil to egress
// directly. Implemented in package api (which owns config.HTTPSProxyURL +
// lib/egressproxy) and injected into the Manager to avoid an import cycle.
// geoLocation is the provider's region; sessionID becomes the proxy sticky
// session id so browser + attestor share one IP.
type ProxyResolver func(geoLocation, sessionID string) *ProxyConfig

// applyExtensionProxy points the bundled proxy extension's chrome.proxy at cfg
// (or clears it when cfg is nil), by evaluating the extension service worker's
// handleSetProxy/handleClearProxy over CDP. chrome.proxy is the only runtime
// path to (re)configure the proxy on the persistent default context without a
// chromium restart — see images/chromium-headful docs. Credentials are NOT set
// here: the proxy 407 challenge is answered via CDP Fetch.authRequired in
// netcapture (chromium ignores user:pass in the proxy server config, and the
// MV3 webRequest.onAuthRequired path doesn't fire on proxy CONNECT — chromium
// bug 40274579).
//
// Best-effort: a failure is logged by the caller and the session continues
// (direct egress) rather than aborting — losing the proxy is better than losing
// the session, and the stealth banner / proof outcome will surface it.
func applyExtensionProxy(ctx context.Context, client *cdpclient.Client, cfg *ProxyConfig) error {
	swSessionID, err := attachExtensionWorker(ctx, client)
	if err != nil {
		return err
	}

	var expr string
	if cfg == nil {
		expr = "handleClearProxy()"
	} else {
		// scheme/host/port only — the extension's handleSetProxy sets
		// fixed_servers; bypassList keeps loopback direct.
		args, _ := json.Marshal(map[string]any{
			"scheme": cfg.Scheme,
			"host":   cfg.Host,
			"port":   cfg.Port,
		})
		expr = fmt.Sprintf("handleSetProxy(%s)", args)
	}

	params := map[string]any{
		"expression":    expr,
		"awaitPromise":  true,
		"returnByValue": true,
	}
	if _, err := client.Call(ctx, "Runtime.evaluate", params, swSessionID); err != nil {
		return fmt.Errorf("evaluate %s in proxy SW: %w", expr, err)
	}
	return nil
}

// attachExtensionWorker finds the proxy extension's service-worker target and
// attaches (flattened), returning the CDP sessionId to evaluate against. The
// proxy extension is the only one loaded (--load-extension in wrapper.sh), so
// any chrome-extension:// service_worker target is ours.
//
// Two gotchas this handles: (1) Target.getTargets' DEFAULT filter omits
// service_worker — we pass an explicit all-types filter ([{}]) and also enable
// discovery; (2) an MV3 service worker that has idled out isn't a live target,
// so we retry briefly to give a just-started/just-woken worker time to appear.
// On failure the error lists the targets we DID see, so a dormant-vs-not-loaded
// problem is diagnosable from the log.
func attachExtensionWorker(ctx context.Context, client *cdpclient.Client) (string, error) {
	// Best-effort: make all target types discoverable on this connection.
	_, _ = client.Call(ctx, "Target.setDiscoverTargets", map[string]any{"discover": true}, "")

	type targetInfo struct {
		TargetID string `json:"targetId"`
		Type     string `json:"type"`
		URL      string `json:"url"`
	}
	allTypesFilter := map[string]any{"filter": []map[string]any{{}}}

	var seen []targetInfo
	targetID := ""
	// Retry: a dormant/just-woken MV3 worker may take a moment to register.
	for attempt := 0; attempt < 5 && targetID == ""; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		raw, err := client.Call(ctx, "Target.getTargets", allTypesFilter, "")
		if err != nil {
			return "", fmt.Errorf("Target.getTargets: %w", err)
		}
		var res struct {
			TargetInfos []targetInfo `json:"targetInfos"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return "", fmt.Errorf("decode targets: %w", err)
		}
		seen = res.TargetInfos
		for _, t := range res.TargetInfos {
			if (t.Type == "service_worker" || t.Type == "worker") &&
				strings.HasPrefix(t.URL, "chrome-extension://") {
				targetID = t.TargetID
				break
			}
		}
	}
	if targetID == "" {
		var b strings.Builder
		for _, t := range seen {
			fmt.Fprintf(&b, " [%s %s]", t.Type, t.URL)
		}
		return "", fmt.Errorf("proxy extension service worker not found among %d targets:%s", len(seen), b.String())
	}

	attachRaw, err := client.Call(ctx, "Target.attachToTarget",
		map[string]any{"targetId": targetID, "flatten": true}, "")
	if err != nil {
		return "", fmt.Errorf("attach to proxy SW: %w", err)
	}
	var attach struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attachRaw, &attach); err != nil || attach.SessionID == "" {
		return "", fmt.Errorf("decode attach sessionId: %w", err)
	}
	return attach.SessionID, nil
}
