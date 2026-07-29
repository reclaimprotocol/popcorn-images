package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsRetryableProveError(t *testing.T) {
	// The shape the TEE library produces: terminateConnectionWithError pushes the
	// reason plus the TEE_K message into completionChan, and
	// ExecuteCompleteProtocol wraps that before returning it.
	teeTimeout := fmt.Errorf("protocol terminated with error: %v",
		fmt.Errorf("Received error from TEE_K: %v",
			errors.New("TEE_K error: TLS handshake failed: failed to read server ChangeCipherSpec: failed to read ChangeCipherSpec header: timeout reading from websocket")))

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"tee websocket read timeout", teeTimeout, true},
		{"core protocol timed out", errors.New("core TEE protocol timed out"), true},
		{"our own budget expiring", errProveTimeout, true},
		{"context deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"net timeout", fmt.Errorf("dial: %w", &net.DNSError{Err: "i/o", IsTimeout: true}), true},
		{"panic recovery", errors.New("internal error: protocol execution panicked"), false},
		{"connect failure", errors.New("failed to connect to TEE_K: connection refused"), false},
		{"attestor rejection", errors.New("attestor submission failed: invalid claim"), false},
		{"redaction failure", errors.New("OPRF processing failed: range out of bounds"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isRetryableProveError(tt.err))
		})
	}
}
