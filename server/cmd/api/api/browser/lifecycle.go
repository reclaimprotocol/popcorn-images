package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onkernel/kernel-images/server/lib/cdpclient"
	"github.com/onkernel/kernel-images/server/lib/logger"
)

const upstreamWaitTimeout = 10 * time.Second

// Start attaches to the local Chromium over CDP and begins a single session.
// It returns ErrSessionExists if one is already active. loginUrl navigation is
// best-effort (non-fatal), matching the portal worker's loadLoginPage.
func (m *Manager) Start(ctx context.Context, cfg *SessionConfig) (*Session, error) {
	log := logger.FromContext(ctx)

	if cfg == nil || cfg.ProviderConfig == nil {
		return nil, fmt.Errorf("provider_config is required")
	}

	// Enforce single-session up front.
	m.mu.Lock()
	if m.current != nil {
		m.mu.Unlock()
		return nil, ErrSessionExists
	}
	m.mu.Unlock()

	// Resolve the live CDP URL (wait briefly if the upstream isn't ready yet).
	url := m.upstream.Current()
	if url == "" {
		var err error
		url, err = m.upstream.WaitForInitial(upstreamWaitTimeout)
		if err != nil {
			return nil, fmt.Errorf("devtools upstream not available: %w", err)
		}
	}

	client, err := m.dial(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("dial cdp: %w", err)
	}
	cdpSess, err := cdpclient.Attach(ctx, client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("attach to page target: %w", err)
	}
	// NOTE: we intentionally do NOT call Runtime.enable here. Runtime.enable is
	// the canonical CDP-detection signal (anti-bot pages — reCAPTCHA, Cloudflare,
	// DataDome — detect it via the console-argument serialization leak and lower
	// the bot score, causing "reCAPTCHA score too low" / challenge failures).
	// Nothing on this session consumes Runtime events: every use is a
	// default-context Runtime.evaluate (returnByValue), which works without the
	// domain enabled — netcapture already evaluates the same way with no
	// Runtime.enable. Keeping the domain disabled removes the leak during the
	// capture/proof flow that runs while the user is on the login page.

	// Apply viewport if provided.
	if vp := cfg.ProviderConfig.Viewport; vp != nil && vp.Width > 0 && vp.Height > 0 {
		if err := cdpSess.SetViewport(ctx, vp.Width, vp.Height); err != nil {
			log.Warn("set viewport failed", "err", err)
		}
	}

	sid := cfg.SessionID
	if sid == "" {
		sid = uuid.NewString()
	}

	// Egress proxy. Resolve the per-session proxy (host:port + sticky-session
	// credentials keyed by sid, so the browser shares the TEE proof's exit IP)
	// and point the proxy extension at it — or clear it — BEFORE any navigation,
	// so the login page loads through the proxied IP. useProxy gates engagement;
	// we always reconcile (set or clear) so a prior session's proxy can't leak
	// into a useProxy=false session on the same persistent chromium. Auth is
	// answered later via CDP Fetch.authRequired (see netcapture).
	var proxyCfg *ProxyConfig
	if m.resolveProxy != nil {
		if cfg.ProviderConfig.UseProxy {
			proxyCfg = m.resolveProxy(cfg.ProviderConfig.GeoLocation, sid)
		}
		if err := applyExtensionProxy(ctx, client, proxyCfg); err != nil {
			log.Warn("apply egress proxy failed (continuing direct)", "err", err, "use_proxy", cfg.ProviderConfig.UseProxy)
		}
	}

	now := time.Now()
	sess := &Session{
		SessionID:    sid,
		WsEndpoint:   url,
		CreatedAt:    now,
		LastActivity: now,
		IsConnected:  true,
		Config:       cfg,
		cdp:          cdpSess,
		client:       client,
	}

	m.mu.Lock()
	// Re-check in case of a race between the early check and attach.
	if m.current != nil {
		m.mu.Unlock()
		_ = cdpSess.Detach(ctx)
		_ = client.Close()
		return nil, ErrSessionExists
	}
	m.current = sess
	m.claims = nil
	report := m.boundReporter(sess)
	// Browser attached + session created → report USER_INIT_VERIFICATION.
	report(statusUserInitVerification, nil)
	// When usePortalTEE resolved false, run capture-only (no Prover).
	prover := m.prover
	if cfg.ProofsDisabled {
		prover = nil
	}
	// Start network/event capture on its own CDP connection. Proof matchers
	// come from provider_config.requestData (empty == capture-only).
	m.capture = newNetCapture(m.upstream, log, sess.SessionID, m.bus.publish,
		cfg.ProviderConfig.RequestData, prover, m.AddClaim,
		cfg.ProviderConfig.CustomInjection != "", report)
	// Hand the resolved proxy to capture so its CDP loop enables Fetch and
	// answers the proxy 407 with the session's credentials.
	m.capture.proxy = proxyCfg
	m.capture.Start()
	m.mu.Unlock()

	// Inject the page runtime (window.Reclaim, login detection, interception,
	// customInjection) before navigating, so init scripts run on the login page.
	m.setupPageEnvironment(ctx, sess)

	// Navigate to the login page (best-effort).
	if lu := cfg.ProviderConfig.LoginURL; lu != "" && lu != "about:blank" {
		if err := m.loadLoginPage(ctx, sess, lu); err != nil {
			log.Warn("loadLoginPage failed (non-fatal)", "err", err, "url", lu)
		}
	}

	// Live page available → report USER_STARTED_VERIFICATION.
	report(statusUserStartedVerification, nil)

	log.Info("browser session started", "session_id", sid, "ws_endpoint", url)
	return sess, nil
}

// cdp returns the active session's CDP handle, used by actions.
func (m *Manager) loadLoginPage(ctx context.Context, sess *Session, loginURL string) error {
	const maxRetries = 3
	const baseDelay = time.Second

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		navCtx, cancel := context.WithTimeout(ctx, navTimeoutMS*time.Millisecond)
		err := sess.cdp.Navigate(navCtx, loginURL)
		if err == nil {
			_ = waitReadyState(navCtx, sess, defaultWaitUntil, navTimeoutMS*time.Millisecond)
			cancel()
			return nil
		}
		cancel()
		// ERR_ABORTED commonly means a download/redirect started — treat as soft
		// success, matching the portal worker.
		if strings.Contains(err.Error(), "ERR_ABORTED") {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(baseDelay * time.Duration(attempt)):
		}
	}
	return lastErr
}

// Close tears down the active session. It is idempotent: closing when no
// session is active returns (false, nil).
func (m *Manager) Close(ctx context.Context) (bool, error) {
	m.mu.Lock()
	sess := m.current
	capture := m.capture
	m.current = nil
	m.capture = nil
	m.mu.Unlock()

	if sess == nil {
		return false, nil
	}
	if capture != nil {
		capture.Stop()
	}
	if sess.cdp != nil {
		_ = sess.cdp.Detach(ctx)
	}
	if sess.client != nil {
		_ = sess.client.Close()
	}
	sess.IsConnected = false
	m.bus.publish(newEvent(EvSessionClosed, sess.SessionID, SessionClosedData{Reason: "client"}))
	logger.FromContext(ctx).Info("browser session closed", "session_id", sess.SessionID)
	return true, nil
}
