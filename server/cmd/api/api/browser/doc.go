// Package browser is the in-image rewrite of the portal "browser-events"
// worker. It attaches to the local Chromium over CDP (no remote provider:
// Browserbase/Kernel/Browserless are dropped) and drives a single session per
// image: lifecycle (start/close), selector-based actions (navigate, click,
// type, submit, screenshot), and — in later phases — network capture, an event
// stream, and reclaim-tee validation/proof.
//
// Decoupling: the manager talks to the DevTools upstream and the CDP dialer
// through small interfaces (UpstreamCurrenter, DialFunc) so it doesn't import
// devtoolsproxy directly and stays unit-testable. The concrete wiring lives in
// package api (browser_endpoints.go), which adapts *devtoolsproxy.UpstreamManager
// and cdpclient.Dial.
//
// See plans/migrate-browser-events-to-popcorn.md and
// plans/spec-foundation-phases-0-2.md for the migration plan and the spec this
// implements.
package browser
