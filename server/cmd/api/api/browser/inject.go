package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/onkernel/kernel-images/server/lib/logger"
)

// setupPageEnvironment injects the browser-side runtime before navigation,
// mirroring the portal worker's setupUserScripts order:
//
//	reclaim_env -> login_script -> set window.Reclaim.provider/parameters ->
//	(HAWKEYE: hawkeye | NONE: nothing | default: interceptor_notifier) ->
//	customInjection
//
// Each script is registered on new documents (persists across navigations) and
// also evaluated immediately for the current document. Failures are logged, not
// fatal (the portal worker is equally tolerant).
//
// NOTE: the post-navigation re-injection "safety nets" the portal worker used
// existed to work around a remote gateway dropping init scripts. In-image we're
// attached directly to the local Chromium, so addScriptToEvaluateOnNewDocument
// is reliable and the safety nets are omitted.
func (m *Manager) setupPageEnvironment(ctx context.Context, sess *Session) {
	log := logger.FromContext(ctx)
	pc := sess.Config.ProviderConfig
	if pc == nil {
		return
	}

	if err := sess.cdp.PageEnable(ctx); err != nil {
		log.Warn("Page.enable failed before injection", "err", err)
	}

	inject := func(name, src string) {
		if strings.TrimSpace(src) == "" {
			return
		}
		if err := sess.cdp.AddInitScript(ctx, src); err != nil {
			log.Warn("addInitScript failed", "script", name, "err", err)
		}
		if _, err := sess.cdp.Evaluate(ctx, src); err != nil {
			log.Warn("inject-now failed", "script", name, "err", err)
		}
	}

	// 1. Runtime first (installs window.Reclaim), then login detection.
	inject("reclaim_env", scriptReclaimEnv)
	inject("login_script", scriptLoginScript)

	// 2. Expose the provider config + parameters on window.Reclaim (excluding
	// the portal's delete-list: customInjection/userAgent/viewport/proxies).
	if expr := reclaimArgsExpr(pc, sess.Config.Parameters); expr != "" {
		inject("reclaim_args", expr)
	}

	// 3. Interception script per injectionType.
	switch strings.ToUpper(pc.InjectionType) {
	case "HAWKEYE":
		inject("hawkeye", scriptHawkeye)
	case "NONE":
		// no interception script
	default:
		inject("interceptor_notifier", scriptInterceptorNotifier)
	}

	// 4. Provider-supplied custom injection.
	if pc.CustomInjection != "" {
		inject("customInjection", pc.CustomInjection)
	}
}

// reclaimArgsExpr builds a JS statement that sets window.Reclaim.provider and
// window.Reclaim.parameters. The provider object excludes the delete-list keys.
func reclaimArgsExpr(pc *ProviderConfig, params map[string]string) string {
	provider := map[string]any{}
	if pc.ProviderID != "" {
		provider["providerId"] = pc.ProviderID
	}
	if pc.AppID != "" {
		provider["appId"] = pc.AppID
	}
	if pc.LoginURL != "" {
		provider["loginUrl"] = pc.LoginURL
	}
	if pc.InjectionType != "" {
		provider["injectionType"] = pc.InjectionType
	}
	if pc.LogLevel != "" {
		provider["logLevel"] = pc.LogLevel
	}
	if len(pc.RequestData) > 0 {
		provider["requestData"] = pc.RequestData
	}
	for k, v := range pc.Extra {
		provider[k] = v
	}

	if params == nil {
		params = map[string]string{}
	}
	pj, err := json.Marshal(provider)
	if err != nil {
		return ""
	}
	paramj, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(
		"(function(){ if (window.Reclaim) { window.Reclaim.provider = %s; window.Reclaim.parameters = %s; } })()",
		string(pj), string(paramj),
	)
}
