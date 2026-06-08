package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onkernel/kernel-images/server/cmd/api/circuits"
	"github.com/onkernel/kernel-images/server/lib/logger"
	oapi "github.com/onkernel/kernel-images/server/lib/oapi"
	"github.com/reclaimprotocol/reclaim-tee/client"
)

// reclaimConfigJSON is the structure for optional config overrides
type reclaimConfigJSON struct {
	TEEKUrl     string `json:"teekUrl,omitempty"`
	TEETUrl     string `json:"teetUrl,omitempty"`
	AttestorUrl string `json:"attestorUrl,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
}

// ReclaimProve executes the TEE+MPC proof protocol
func (s *ApiService) ReclaimProve(ctx context.Context, req oapi.ReclaimProveRequestObject) (oapi.ReclaimProveResponseObject, error) {
	log := logger.FromContext(ctx)

	// Get TEE URLs from config (already includes env var overrides via envconfig)
	teekUrl := s.config.TEEKUrl
	teetUrl := s.config.TEETUrl
	attestorUrl := s.config.AttestorUrl
	var requestID string

	// Apply request-level config overrides if provided
	if req.Body.ConfigJson != nil && *req.Body.ConfigJson != "" {
		var cfg reclaimConfigJSON
		if err := json.Unmarshal([]byte(*req.Body.ConfigJson), &cfg); err == nil {
			if cfg.TEEKUrl != "" {
				teekUrl = cfg.TEEKUrl
			}
			if cfg.TEETUrl != "" {
				teetUrl = cfg.TEETUrl
			}
			if cfg.AttestorUrl != "" {
				attestorUrl = cfg.AttestorUrl
			}
			if cfg.RequestID != "" {
				requestID = cfg.RequestID
			}
		}
	}

	// Validate requestId length, fall back to generated UUID if not provided
	if requestID == "" {
		requestID = uuid.New().String()
	} else if len(requestID) > 100 {
		return oapi.ReclaimProve400JSONResponse{
			BadRequestErrorJSONResponse: oapi.BadRequestErrorJSONResponse{
				Message: "requestId exceeds maximum length of 100 characters",
			},
		}, nil
	}

	log.Info("starting reclaim prove", "request_id", requestID)

	log.Info("using TEE configuration",
		"teek_url", teekUrl,
		"teet_url", teetUrl,
		"attestor_url", attestorUrl,
	)

	// Build config JSON for the client library (resolved URLs + requestID).
	clientConfigJSON, err := json.Marshal(reclaimConfigJSON{
		TEEKUrl:     teekUrl,
		TEETUrl:     teetUrl,
		AttestorUrl: attestorUrl,
		RequestID:   requestID,
	})
	if err != nil {
		log.Error("failed to marshal client config", "err", err)
		return oapi.ReclaimProve500JSONResponse{
			InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
				Message: "failed to prepare client configuration",
			},
		}, nil
	}

	claim, err := s.executeReclaimProve(ctx, req.Body.ProviderParamsJson, string(clientConfigJSON))
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "invalid provider parameters") {
			return oapi.ReclaimProve400JSONResponse{
				BadRequestErrorJSONResponse: oapi.BadRequestErrorJSONResponse{Message: msg},
			}, nil
		}
		log.Error("proof execution failed", "request_id", requestID, "err", err)
		return oapi.ReclaimProve500JSONResponse{
			InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{Message: msg},
		}, nil
	}

	log.Info("proof execution completed", "request_id", requestID, "identifier", claim.Claim.Identifier)
	return oapi.ReclaimProve200JSONResponse{
		SessionId: requestID,
		Claim:     mapClaimToOapi(claim.Claim),
		Signature: mapSignatureToOapi(claim.Signature),
	}, nil
}

// executeReclaimProve runs the TEE+MPC proof protocol for the given
// provider_params_json + client config json. It is shared by the HTTP endpoint
// and the in-image browser proof pipeline. A 5-minute timeout and a panic
// recover guard the external library.
func (s *ApiService) executeReclaimProve(ctx context.Context, providerParamsJSON, clientConfigJSON string) (*client.ClaimWithSignatures, error) {
	// Setup ZK callback (idempotent, only runs once).
	circuits.SetupZKCallback()

	var providerData client.ProviderRequestData
	if err := json.Unmarshal([]byte(providerParamsJSON), &providerData); err != nil {
		return nil, fmt.Errorf("invalid provider parameters JSON: %w", err)
	}

	reclaimClient, err := client.NewReclaimClientFromJSON(providerParamsJSON, clientConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid provider parameters: %w", err)
	}

	proofCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	type result struct {
		claim *client.ClaimWithSignatures
		err   error
	}
	resultCh := make(chan result, 1)
	log := logger.FromContext(ctx)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Surface the panic reason (the recovered value) and log the full
				// stack so the failure is diagnosable instead of opaque.
				log.Error("ExecuteCompleteProtocol panicked",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()))
				resultCh <- result{err: fmt.Errorf("protocol execution panicked: %v", r)}
			}
		}()
		claim, err := reclaimClient.ExecuteCompleteProtocol(&providerData)
		resultCh <- result{claim: claim, err: err}
	}()

	select {
	case <-proofCtx.Done():
		// Don't race Close() with an in-flight protocol: clean up in the
		// background after the goroutine drains (or a grace period elapses).
		go func() {
			select {
			case <-resultCh:
			case <-time.After(10 * time.Second):
			}
			reclaimClient.Close()
		}()
		return nil, fmt.Errorf("proof execution timed out")
	case res := <-resultCh:
		reclaimClient.Close()
		if res.err != nil {
			return nil, fmt.Errorf("proof execution failed: %w", res.err)
		}
		return res.claim, nil
	}
}

// buildReclaimClientConfigJSON builds the client config JSON from server config
// + a per-session requestID (used by the browser proof pipeline).
func (s *ApiService) buildReclaimClientConfigJSON(requestID string) (string, error) {
	b, err := json.Marshal(reclaimConfigJSON{
		TEEKUrl:     s.config.TEEKUrl,
		TEETUrl:     s.config.TEETUrl,
		AttestorUrl: s.config.AttestorUrl,
		RequestID:   requestID,
	})
	return string(b), err
}

func mapClaimToOapi(claim interface{}) oapi.ReclaimClaim {
	// The claim is a protobuf message, we need to extract fields
	// Using type assertion with the actual proto type
	type providerClaimData interface {
		GetProvider() string
		GetParameters() string
		GetOwner() string
		GetTimestampS() uint32
		GetContext() string
		GetIdentifier() string
		GetEpoch() uint32
	}

	if c, ok := claim.(providerClaimData); ok {
		provider := c.GetProvider()
		parameters := c.GetParameters()
		owner := c.GetOwner()
		timestampS := int(c.GetTimestampS())
		context := c.GetContext()
		identifier := c.GetIdentifier()
		epoch := int(c.GetEpoch())

		return oapi.ReclaimClaim{
			Provider:   &provider,
			Parameters: &parameters,
			Owner:      &owner,
			TimestampS: &timestampS,
			Context:    &context,
			Identifier: &identifier,
			Epoch:      &epoch,
		}
	}

	return oapi.ReclaimClaim{}
}

func mapSignatureToOapi(sig interface{}) oapi.ReclaimSignature {
	// The signature is a protobuf message
	type claimSignature interface {
		GetAttestorAddress() string
		GetClaimSignature() []byte
		GetResultSignature() []byte
	}

	if s, ok := sig.(claimSignature); ok {
		attestorAddr := s.GetAttestorAddress()
		claimSig := base64.StdEncoding.EncodeToString(s.GetClaimSignature())
		resultSig := base64.StdEncoding.EncodeToString(s.GetResultSignature())

		return oapi.ReclaimSignature{
			AttestorAddress: &attestorAddr,
			ClaimSignature:  &claimSig,
			ResultSignature: &resultSig,
		}
	}

	return oapi.ReclaimSignature{}
}
