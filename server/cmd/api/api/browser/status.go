package browser

// SessionStatus is the live status of the active session: login detection and
// proof-generation progress.
type SessionStatus struct {
	SessionID  string         `json:"session_id"`
	CurrentURL string         `json:"current_url,omitempty"`
	Login      LoginStatus    `json:"login"`
	Proofs     ProofStatus    `json:"proofs"`
	Claims     []*ClaimResult `json:"claims"`
}

// LoginStatus reports whether the page currently looks like it needs a login
// interaction. Indicator is "none" (no login needed / logged in), "url" or
// "element" (login required), "timeout", or "unknown" (not yet determined).
type LoginStatus struct {
	Indicator           string `json:"indicator"`
	RequiresInteraction bool   `json:"requires_interaction"`
}

// ProofStatus summarizes proof generation. Expected is the number of configured
// requestData matchers; the rest reflect attempts seen so far.
type ProofStatus struct {
	Expected   int `json:"expected"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	InProgress int `json:"in_progress"`
}

// Status returns the active session's status, or nil if no session is active.
func (m *Manager) Status() *SessionStatus {
	m.mu.Lock()
	sess := m.current
	capture := m.capture
	claims := make([]*ClaimResult, len(m.claims))
	copy(claims, m.claims)
	m.mu.Unlock()

	if sess == nil {
		return nil
	}

	st := &SessionStatus{SessionID: sess.SessionID, Claims: claims}

	if sess.Config != nil && sess.Config.ProviderConfig != nil {
		st.Proofs.Expected = len(sess.Config.ProviderConfig.RequestData)
	}
	for _, c := range claims {
		if c.Error != "" {
			st.Proofs.Failed++
		} else {
			st.Proofs.Succeeded++
		}
	}

	indicator := "unknown"
	if capture != nil {
		st.CurrentURL = capture.CurrentURL()
		if li := capture.LoginIndicator(); li != "" {
			indicator = li
		}
		inProgress := capture.ProvenCount() - (st.Proofs.Succeeded + st.Proofs.Failed)
		if inProgress > 0 {
			st.Proofs.InProgress = inProgress
		}
	}
	st.Login.Indicator = indicator
	st.Login.RequiresInteraction = indicator == "url" || indicator == "element"

	return st
}
