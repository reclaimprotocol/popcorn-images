// Command bootstrap-check exercises the in-image bootstrap path against a live
// Reclaim backend, without needing a running browser. It replicates the
// sequence in api.resolveSessionConfig:
//
//	GetSession -> GetProvider -> FeatureFlagBool(usePortalTEE)
//
// Usage:
//
//	go run ./lib/reclaimbackend/cmd/bootstrap-check -session <SESSION_ID>
//	go run ./lib/reclaimbackend/cmd/bootstrap-check -session <ID> -backend https://staging.example.org
//
// Flags:
//
//	-session   live, non-terminal session id (required)
//	-backend   backend base URL (default: $RECLAIM_BACKEND_URL or prod)
//	-provider  override provider id (skip the one from getSession)
//	-version   override provider version (skip the one from getSession)
//	-flag      feature flag name to gate proofs on (default: usePortalTEE)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/onkernel/kernel-images/server/lib/reclaimbackend"
)

func main() {
	var (
		sessionID   = flag.String("session", "", "live session id (required)")
		backendURL  = flag.String("backend", os.Getenv("RECLAIM_BACKEND_URL"), "backend base URL (default: prod)")
		providerOvr = flag.String("provider", "", "override provider id")
		versionOvr  = flag.String("version", "", "override provider version")
		flagName    = flag.String("flag", "usePortalTEE", "feature flag gating proofs")
	)
	flag.Parse()

	if *sessionID == "" {
		fmt.Fprintln(os.Stderr, "error: -session is required")
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := reclaimbackend.New(*backendURL)
	fmt.Printf("backend: %s\n\n", deref(*backendURL, "https://api.reclaimprotocol.org (default)"))

	// 1. getSession -----------------------------------------------------------
	fmt.Println("== getSession ==")
	sess, err := c.GetSession(ctx, *sessionID)
	if err != nil {
		fatal("getSession failed: %v", err)
	}
	sd := sess.Session
	providerID := sd.ProviderID
	if providerID == "" {
		providerID = sess.ProviderID
	}
	version := sd.ProviderVersionString()
	fmt.Printf("  appId:      %s\n", sd.AppID)
	fmt.Printf("  providerId: %s\n", providerID)
	fmt.Printf("  version:    %q\n", version)
	fmt.Printf("  statusV2:   %s\n", sd.StatusV2)
	fmt.Printf("  proofs:     %d\n", len(sd.Proofs))
	if sd.IsTerminal() {
		fatal("session is in a terminal state (statusV2=%s) -> bootstrap would 409", sd.StatusV2)
	}

	if *providerOvr != "" {
		providerID = *providerOvr
		fmt.Printf("  (provider overridden -> %s)\n", providerID)
	}
	if *versionOvr != "" {
		version = *versionOvr
		fmt.Printf("  (version overridden -> %q)\n", version)
	}

	// 2. getProvider ----------------------------------------------------------
	fmt.Println("\n== getProvider ==")
	raw, err := c.GetProvider(ctx, providerID, version)
	if err != nil {
		fatal("getProvider failed: %v", err)
	}
	var dto struct {
		ProviderID  string            `json:"providerId"`
		AppID       string            `json:"appId"`
		LoginURL    string            `json:"loginUrl"`
		RequestData []json.RawMessage `json:"requestData"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		fatal("decode provider config: %v", err)
	}
	fmt.Printf("  loginUrl:        %s\n", dto.LoginURL)
	fmt.Printf("  requestData len: %d\n", len(dto.RequestData))
	hasRequestData := len(dto.RequestData) > 0

	appID := sd.AppID
	if dto.AppID != "" {
		appID = dto.AppID
	}

	// 3. feature flag ---------------------------------------------------------
	fmt.Printf("\n== featureFlag(%s) ==\n", *flagName)
	enabled := c.FeatureFlagBool(ctx, *flagName, appID, providerID)
	fmt.Printf("  %s = %v  (false also means: backend error / flag missing)\n", *flagName, enabled)

	// 4. resolved outcome (matches resolveSessionConfig) ----------------------
	proofsDisabled := hasRequestData && !enabled
	fmt.Println("\n== resolved SessionConfig ==")
	fmt.Printf("  ProofsDisabled: %v\n", proofsDisabled)
	switch {
	case !hasRequestData:
		fmt.Println("  -> no requestData: capture-only regardless of flag")
	case proofsDisabled:
		fmt.Println("  -> requestData present but flag off: CAPTURE-ONLY (no proofs)")
	default:
		fmt.Println("  -> requestData present + flag on: PROOF GENERATION enabled")
	}
	fmt.Println("\nOK")
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

func deref(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
