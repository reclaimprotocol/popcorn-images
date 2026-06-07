package browser

import _ "embed"

// Injected browser-side scripts, ported from the portal worker's scripts/.
// capmonster.js (captcha) and stealth_init.js are intentionally NOT ported
// (decisions #5/#6).
//
// reclaim_env.js installs window.Reclaim (the page runtime customInjection
// providers call into) and login_script.js registers login detection. hawkeye/
// interceptor do *in-page* network interception feeding window.Reclaim's
// outbox — that role is superseded by the CDP-based capture in netcapture.go,
// so they're injected for injectionType parity but their outbox is not yet
// consumed in-image (see inject.go).

//go:embed scripts/reclaim_env.js
var scriptReclaimEnv string

//go:embed scripts/login_script.js
var scriptLoginScript string

//go:embed scripts/hawkeye.js
var scriptHawkeye string

//go:embed scripts/interceptor_notifier.js
var scriptInterceptorNotifier string
