package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

	// Setup ZK callback (idempotent, only runs once)
	circuits.SetupZKCallback()

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

	// Parse provider data for ExecuteCompleteProtocol
	var providerData client.ProviderRequestData
	if err := json.Unmarshal([]byte(req.Body.ProviderParamsJson), &providerData); err != nil {
		log.Error("failed to parse provider params", "err", err)
		return oapi.ReclaimProve400JSONResponse{
			BadRequestErrorJSONResponse: oapi.BadRequestErrorJSONResponse{
				Message: fmt.Sprintf("invalid provider parameters JSON: %v", err),
			},
		}, nil
	}

	// Build config JSON for the client library
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

	// Create reclaim client from JSON
	reclaimClient, err := client.NewReclaimClientFromJSON(
		req.Body.ProviderParamsJson,
		string(clientConfigJSON),
	)
	if err != nil {
		log.Error("failed to create reclaim client", "err", err)
		return oapi.ReclaimProve400JSONResponse{
			BadRequestErrorJSONResponse: oapi.BadRequestErrorJSONResponse{
				Message: fmt.Sprintf("invalid provider parameters: %v", err),
			},
		}, nil
	}

	// Create a context with timeout (5 minutes for proof generation)
	proofCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Execute protocol in a goroutine so we can handle timeout
	type result struct {
		claim *client.ClaimWithSignatures
		err   error
	}
	resultCh := make(chan result, 1)

	go func() {
		// Recover from panics in the external library to prevent server crash
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in ExecuteCompleteProtocol", "request_id", requestID, "panic", r)
				resultCh <- result{err: fmt.Errorf("internal error: protocol execution panicked")}
			}
		}()
		claim, err := reclaimClient.ExecuteCompleteProtocol(&providerData)
		resultCh <- result{claim: claim, err: err}
	}()

	// Wait for result or timeout
	var timedOut bool
	select {
	case <-proofCtx.Done():
		timedOut = true
		log.Error("proof execution timed out, waiting for goroutine cleanup", "request_id", requestID)
	case res := <-resultCh:
		// Close client after goroutine completes
		reclaimClient.Close()

		if res.err != nil {
			log.Error("proof execution failed", "request_id", requestID, "err", res.err)
			return oapi.ReclaimProve500JSONResponse{
				InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
					Message: fmt.Sprintf("proof execution failed: %v", res.err),
				},
			}, nil
		}

		log.Info("proof execution completed", "request_id", requestID, "identifier", res.claim.Claim.Identifier)

		// Map result to response
		return oapi.ReclaimProve200JSONResponse{
			SessionId: requestID,
			Claim:     mapClaimToOapi(res.claim.Claim),
			Signature: mapSignatureToOapi(res.claim.Signature),
		}, nil
	}

	// If we timed out, wait for the goroutine to complete before closing
	// to avoid racing Close() with an in-flight protocol
	if timedOut {
		// Wait for goroutine with a grace period, then close regardless
		select {
		case <-resultCh:
			log.Info("goroutine completed after timeout", "request_id", requestID)
		case <-time.After(10 * time.Second):
			log.Warn("goroutine did not complete within grace period, closing anyway", "request_id", requestID)
		}
		reclaimClient.Close()

		return oapi.ReclaimProve500JSONResponse{
			InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
				Message: "proof execution timed out",
			},
		}, nil
	}

	// Should not reach here, but satisfy compiler
	return oapi.ReclaimProve500JSONResponse{
		InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
			Message: "unexpected error",
		},
	}, nil
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
