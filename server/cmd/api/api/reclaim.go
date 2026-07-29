package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onkernel/kernel-images/server/cmd/api/circuits"
	"github.com/onkernel/kernel-images/server/lib/logger"
	oapi "github.com/onkernel/kernel-images/server/lib/oapi"
	"github.com/reclaimprotocol/reclaim-tee/client"
)

// reclaimConfigJSON is the structure for optional config overrides. It mirrors
// client.ConfigJSON: the TEE_K/TEE_T pair is resolved by the router, so only the
// router URL is configurable.
type reclaimConfigJSON struct {
	RouterUrl   string `json:"routerUrl,omitempty"`
	AttestorUrl string `json:"attestorUrl,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
}

const (
	// proveMaxAttempts bounds how many times the TEE protocol runs for a single
	// /prove call: the initial attempt plus two retries.
	proveMaxAttempts = 3

	// proveTotalTimeout is the budget shared by all attempts, so adding retries
	// does not extend the worst-case latency of a /prove call.
	proveTotalTimeout = 5 * time.Minute

	// proveRetryDelay gives a flaky TEE connection a moment to settle before the
	// next attempt reconnects.
	proveRetryDelay = 2 * time.Second

	// proveCleanupGrace is how long we wait for the protocol goroutine to unwind
	// after the budget expires before closing the client underneath it.
	proveCleanupGrace = 10 * time.Second
)

// errProveTimeout marks the case where our own budget expired rather than the
// protocol returning an error.
var errProveTimeout = errors.New("proof execution timed out")

// isRetryableProveError reports whether err is a transient timeout worth another
// attempt. The TEE library terminates the session on timeouts such as
// "timeout reading from websocket" and surfaces them through
// ExecuteCompleteProtocol, but it formats the chain with %v rather than %w, so
// the wrapped error values are unrecoverable and the message is all we have.
func isRetryableProveError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out")
}

// ReclaimProve executes the TEE+MPC proof protocol
func (s *ApiService) ReclaimProve(ctx context.Context, req oapi.ReclaimProveRequestObject) (oapi.ReclaimProveResponseObject, error) {
	log := logger.FromContext(ctx)

	// Setup ZK callback (idempotent, only runs once)
	circuits.SetupZKCallback()

	// Get TEE URLs from config (already includes env var overrides via envconfig)
	routerUrl := s.config.RouterUrl
	attestorUrl := s.config.AttestorUrl
	var requestID string

	// Apply request-level config overrides if provided
	if req.Body.ConfigJson != nil && *req.Body.ConfigJson != "" {
		var cfg reclaimConfigJSON
		if err := json.Unmarshal([]byte(*req.Body.ConfigJson), &cfg); err == nil {
			if cfg.RouterUrl != "" {
				routerUrl = cfg.RouterUrl
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
		"router_url", routerUrl,
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
		RouterUrl:   routerUrl,
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

	// Create the client up front so invalid provider parameters fail fast with a
	// 400. Construction only performs deterministic validation, so a failure here
	// is never worth retrying.
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

	// Create a context with timeout (5 minutes for proof generation, shared by
	// every attempt)
	proofCtx, cancel := context.WithTimeout(ctx, proveTotalTimeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		claim, err := runProveAttempt(proofCtx, log, requestID, attempt, reclaimClient, &providerData)
		if err == nil {
			log.Info("proof execution completed", "request_id", requestID, "attempt", attempt, "identifier", claim.Claim.Identifier)

			// Map result to response
			return oapi.ReclaimProve200JSONResponse{
				SessionId: requestID,
				Claim:     mapClaimToOapi(claim.Claim),
				Signature: mapSignatureToOapi(claim.Signature),
			}, nil
		}

		// The shared budget is gone, the failure is not a timeout, or we are out
		// of attempts: report it.
		if proofCtx.Err() != nil || !isRetryableProveError(err) || attempt >= proveMaxAttempts {
			log.Error("proof execution failed", "request_id", requestID, "attempt", attempt, "err", err)
			message := fmt.Sprintf("proof execution failed: %v", err)
			if errors.Is(err, errProveTimeout) {
				message = errProveTimeout.Error()
			}
			return oapi.ReclaimProve500JSONResponse{
				InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
					Message: message,
				},
			}, nil
		}

		log.Warn("proof execution hit a timeout, retrying",
			"request_id", requestID,
			"attempt", attempt,
			"max_attempts", proveMaxAttempts,
			"err", err,
		)

		select {
		case <-time.After(proveRetryDelay):
		case <-proofCtx.Done():
			log.Error("proof budget expired before retry", "request_id", requestID, "attempt", attempt)
			return oapi.ReclaimProve500JSONResponse{
				InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
					Message: errProveTimeout.Error(),
				},
			}, nil
		}

		// Each attempt needs its own client: the previous one was closed when the
		// protocol terminated, and its TEE connections are gone with it.
		reclaimClient, err = client.NewReclaimClientFromJSON(
			req.Body.ProviderParamsJson,
			string(clientConfigJSON),
		)
		if err != nil {
			log.Error("failed to create reclaim client for retry", "request_id", requestID, "attempt", attempt+1, "err", err)
			return oapi.ReclaimProve500JSONResponse{
				InternalErrorJSONResponse: oapi.InternalErrorJSONResponse{
					Message: fmt.Sprintf("failed to prepare retry: %v", err),
				},
			}, nil
		}
	}
}

// runProveAttempt executes the TEE protocol once and always closes reclaimClient
// before returning. ExecuteCompleteProtocol cannot be cancelled from the outside,
// so when ctx expires we let the goroutine unwind for a grace period before
// closing the client underneath it and returning errProveTimeout.
func runProveAttempt(
	ctx context.Context,
	log *slog.Logger,
	requestID string,
	attempt int,
	reclaimClient *client.ReclaimClient,
	providerData *client.ProviderRequestData,
) (*client.ClaimWithSignatures, error) {
	log.Info("executing reclaim protocol", "request_id", requestID, "attempt", attempt, "max_attempts", proveMaxAttempts)

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
				log.Error("panic in ExecuteCompleteProtocol", "request_id", requestID, "attempt", attempt, "panic", r)
				resultCh <- result{err: fmt.Errorf("internal error: protocol execution panicked")}
			}
		}()
		claim, err := reclaimClient.ExecuteCompleteProtocol(providerData)
		resultCh <- result{claim: claim, err: err}
	}()

	// Wait for result or timeout
	select {
	case res := <-resultCh:
		// Close client after goroutine completes
		reclaimClient.Close()
		if res.err != nil {
			return nil, res.err
		}
		return res.claim, nil

	case <-ctx.Done():
		log.Error("proof execution timed out, waiting for goroutine cleanup", "request_id", requestID, "attempt", attempt)
		// Wait for the goroutine to complete before closing, to avoid racing
		// Close() with an in-flight protocol
		select {
		case <-resultCh:
			log.Info("goroutine completed after timeout", "request_id", requestID, "attempt", attempt)
		case <-time.After(proveCleanupGrace):
			log.Warn("goroutine did not complete within grace period, closing anyway", "request_id", requestID, "attempt", attempt)
		}
		reclaimClient.Close()
		return nil, errProveTimeout
	}
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
