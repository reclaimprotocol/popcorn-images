package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/onkernel/kernel-images/server/cmd/api/api/browser"
	"github.com/onkernel/kernel-images/server/lib/logger"
)

var (
	// errProviderConfigRequired → 400: neither provider_config nor session_id given.
	errProviderConfigRequired = errors.New("provider_config or session_id is required")
	// errSessionTerminated → 409: the backend session is already finished.
	errSessionTerminated = errors.New("session is already in a terminal state")
)

// resolveSessionConfig produces the SessionConfig for a start request. Two paths:
//
//  1. provider_config supplied (gateway already fetched + decided routing): use
//     it verbatim. No feature-flag gate — the gateway owns that decision.
//  2. provider_config omitted but session_id present: bootstrap from the backend
//     (getSession → getProvider), then gate proof generation on usePortalTEE.
func (s *ApiService) resolveSessionConfig(ctx context.Context, body sessionStartRequest) (*browser.SessionConfig, error) {
	if body.ProviderConfig != nil {
		return body.toConfig(), nil
	}
	if body.SessionID == "" {
		return nil, errProviderConfigRequired
	}
	if s.backend == nil {
		return nil, fmt.Errorf("backend client unavailable")
	}
	log := logger.FromContext(ctx)

	sessResp, err := s.backend.GetSession(ctx, body.SessionID)
	if err != nil {
		return nil, fmt.Errorf("getSession: %w", err)
	}
	if sessResp.Session.IsTerminal() {
		return nil, errSessionTerminated
	}

	providerID := sessResp.Session.ProviderID
	if providerID == "" {
		providerID = sessResp.ProviderID
	}
	appID := sessResp.Session.AppID
	version := sessResp.Session.ProviderVersionString()

	raw, err := s.backend.GetProvider(ctx, providerID, version)
	if err != nil {
		return nil, fmt.Errorf("getProvider: %w", err)
	}
	var dto providerConfigDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return nil, fmt.Errorf("decode provider config: %w", err)
	}
	if dto.ProviderID == "" {
		dto.ProviderID = providerID
	}
	if dto.AppID == "" {
		dto.AppID = appID
	}

	cfg := &browser.SessionConfig{
		SessionID:      body.SessionID,
		Parameters:     body.Parameters,
		ProviderID:     dto.ProviderID,
		AppID:          dto.AppID,
		ProviderConfig: dto.toProviderConfig(),
		DeviceID:       dto.DeviceID,
		DeviceType:     dto.DeviceType,
		OSVersion:      dto.OSVersion,
		PublicIP:       dto.PublicIPAddress,
	}

	// usePortalTEE gates the in-image TEE prove path. When the bootstrap session
	// is not flagged for portal TEE, run capture-only (no proofs).
	if len(cfg.ProviderConfig.RequestData) > 0 {
		if !s.backend.FeatureFlagBool(ctx, "usePortalTEE", cfg.AppID, cfg.ProviderID) {
			cfg.ProofsDisabled = true
			log.Info("usePortalTEE disabled — running capture-only", "session_id", body.SessionID, "provider_id", cfg.ProviderID)
		}
	}
	return cfg, nil
}
