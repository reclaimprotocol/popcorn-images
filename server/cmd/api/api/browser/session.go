package browser

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/onkernel/kernel-images/server/lib/cdpclient"
)

// Errors returned by the manager and surfaced as HTTP status codes by the
// endpoint layer.
var (
	// ErrSessionExists is returned by Start when a session is already active.
	ErrSessionExists = errors.New("a session is already active")
	// ErrNoSession is returned by Execute/Close when no session is active.
	ErrNoSession = errors.New("no active session")
)

// Session is the in-memory state for the single active browser session.
type Session struct {
	SessionID    string
	WsEndpoint   string // the local CDP browser-level URL we attached to
	CreatedAt    time.Time
	LastActivity time.Time
	IsConnected  bool

	Config *SessionConfig

	cdp    *cdpclient.Session // attached page session (flatten)
	client *cdpclient.Client  // owning browser-level client

	mu sync.Mutex
}

func (s *Session) touch() {
	s.mu.Lock()
	s.LastActivity = time.Now()
	s.mu.Unlock()
}

// CurrentURL returns the page's current location.href (best-effort).
func (s *Session) CurrentURL(ctx context.Context) (string, error) {
	if s.cdp == nil {
		return "", nil
	}
	return s.cdp.EvaluateString(ctx, "location.href")
}

// CurrentTitle returns the page's document.title (best-effort).
func (s *Session) CurrentTitle(ctx context.Context) (string, error) {
	if s.cdp == nil {
		return "", nil
	}
	return s.cdp.EvaluateString(ctx, "document.title")
}

// UpstreamCurrenter is the subset of *devtoolsproxy.UpstreamManager the manager
// needs to find the live CDP URL.
type UpstreamCurrenter interface {
	Current() string
	WaitForInitial(timeout time.Duration) (string, error)
}

// DialFunc opens a browser-level CDP client for a DevTools URL. cdpclient.Dial
// satisfies this directly.
type DialFunc func(ctx context.Context, url string) (*cdpclient.Client, error)

// Prover runs a reclaim-tee proof for a matched request: it extracts the
// response variables, assembles provider_params_json, and proves. Injected by
// package api (where reclaim-tee lives) to avoid an import cycle. A nil Prover
// disables proof generation.
type Prover func(ctx context.Context, m RequestMatcher, cap CapturedForProof, requestID string) (*ClaimResult, error)

// Manager owns the single session for this image (one image = one browser =
// one session).
type Manager struct {
	mu      sync.Mutex
	current *Session
	capture *NetCapture
	claims  []*ClaimResult

	upstream UpstreamCurrenter
	dial     DialFunc
	prover   Prover
	bus      *eventBus
}

// NewManager constructs a Manager over a DevTools upstream, a CDP dialer, and an
// optional Prover (nil disables proofs).
func NewManager(upstream UpstreamCurrenter, dial DialFunc, prover Prover) *Manager {
	return &Manager{upstream: upstream, dial: dial, prover: prover, bus: newEventBus()}
}

// AddClaim records a completed proof result.
func (m *Manager) AddClaim(c *ClaimResult) {
	m.mu.Lock()
	m.claims = append(m.claims, c)
	m.mu.Unlock()
}

// Claims returns a snapshot of recorded proof results.
func (m *Manager) Claims() []*ClaimResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ClaimResult, len(m.claims))
	copy(out, m.claims)
	return out
}

// Get returns the active session, or nil.
func (m *Manager) Get() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Subscribe registers an event listener. The caller must Unsubscribe when done.
func (m *Manager) Subscribe() (int, <-chan StreamEvent) {
	return m.bus.subscribe()
}

// Unsubscribe removes an event listener.
func (m *Manager) Unsubscribe(id int) {
	m.bus.unsubscribe(id)
}
