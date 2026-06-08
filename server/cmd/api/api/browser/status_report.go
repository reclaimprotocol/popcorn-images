package browser

import "context"

// Session-status strings reported to the backend. Kept local to the browser
// package (values mirror reclaimbackend.Status*) so this package stays neutral.
const (
	statusUserInitVerification    = "USER_INIT_VERIFICATION"
	statusUserStartedVerification = "USER_STARTED_VERIFICATION"
	statusLoginIndicatorsFound    = "LOGIN_INDICATORS_FOUND"
	statusUserLoggedIn            = "USER_LOGGED_IN"
	statusProofGenerationStarted  = "PROOF_GENERATION_STARTED"
	statusProofGenerationSuccess  = "PROOF_GENERATION_SUCCESS"
	statusProofGenerationFailed   = "PROOF_GENERATION_FAILED"
)

// StatusArgs is one session-status transition to report to the Reclaim backend.
// The browser package fills SessionID + device info from the active session; the
// injected reporter (package api, over reclaimbackend) performs the HTTP call.
type StatusArgs struct {
	SessionID  string
	Status     string
	DeviceID   string
	DeviceType string
	OSVersion  string
	PublicIP   string
	Metadata   map[string]any
}

// StatusReporter reports a session-status transition. Injected by package api to
// avoid importing the backend client here (keeps the browser package neutral). A
// nil reporter disables status reporting.
type StatusReporter func(ctx context.Context, args StatusArgs)

// reportFunc is a session-bound reporter: SessionID + device info are baked in,
// callers pass only the status + optional metadata. Returned by Manager.boundReporter.
type reportFunc func(status string, metadata map[string]any)

// boundReporter returns a reportFunc that fills session/device context for sess
// and dispatches to the manager's reporter. It is always safe to call (no-op
// when the reporter is nil).
func (m *Manager) boundReporter(sess *Session) reportFunc {
	di := sess.deviceInfo()
	return func(status string, metadata map[string]any) {
		if m.reporter == nil {
			return
		}
		m.reporter(context.Background(), StatusArgs{
			SessionID:  sess.SessionID,
			Status:     status,
			DeviceID:   di.DeviceID,
			DeviceType: di.DeviceType,
			OSVersion:  di.OSVersion,
			PublicIP:   di.PublicIP,
			Metadata:   metadata,
		})
	}
}

// DeviceInfo carries the device descriptors sent on every status update.
type DeviceInfo struct {
	DeviceID   string
	DeviceType string
	OSVersion  string
	PublicIP   string
}

// deviceInfo resolves device descriptors for the session, defaulting to the
// portal's fallbacks (sessionId / "Web" / "Web" / "NA").
func (s *Session) deviceInfo() DeviceInfo {
	di := DeviceInfo{
		DeviceID:   s.SessionID,
		DeviceType: "Web",
		OSVersion:  "Web",
		PublicIP:   "NA",
	}
	if s.Config != nil {
		if v := s.Config.DeviceID; v != "" {
			di.DeviceID = v
		}
		if v := s.Config.DeviceType; v != "" {
			di.DeviceType = v
		}
		if v := s.Config.OSVersion; v != "" {
			di.OSVersion = v
		}
		if v := s.Config.PublicIP; v != "" {
			di.PublicIP = v
		}
	}
	return di
}
