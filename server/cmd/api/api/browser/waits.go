package browser

import (
	"context"
	"fmt"
	"time"
)

// pollInterval is how often selector/readyState polls re-evaluate.
const pollInterval = 50 * time.Millisecond

// waitForSelectorAttached polls until the selector is present in the DOM or the
// timeout elapses (Playwright state:"attached").
func waitForSelectorAttached(ctx context.Context, sess *Session, selector string, timeout time.Duration) error {
	return pollUntilTrue(ctx, sess, jsExists(selector), timeout,
		fmt.Sprintf("timeout waiting for selector %q", selector))
}

// waitForSelectorVisible polls until the selector matches a visible element or
// the timeout elapses (Playwright default state:"visible").
func waitForSelectorVisible(ctx context.Context, sess *Session, selector string, timeout time.Duration) error {
	return pollUntilTrue(ctx, sess, jsVisible(selector), timeout,
		fmt.Sprintf("timeout waiting for visible selector %q", selector))
}

func pollUntilTrue(ctx context.Context, sess *Session, expr string, timeout time.Duration, timeoutMsg string) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := sess.cdp.EvaluateBool(ctx, expr)
		if err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s", timeoutMsg)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// waitReadyState waits for document.readyState to reach the state implied by
// waitUntil. "load" requires "complete"; "domcontentloaded" accepts
// "interactive" or "complete". networkidle* is best-effort until network
// capture lands (Phase 3) and is treated as "load".
func waitReadyState(ctx context.Context, sess *Session, waitUntil string, timeout time.Duration) error {
	wantComplete := waitUntil != "domcontentloaded"
	deadline := time.Now().Add(timeout)
	for {
		state, _ := sess.cdp.EvaluateString(ctx, "document.readyState")
		if state == "complete" || (!wantComplete && state == "interactive") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for readyState (%s)", waitUntil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
