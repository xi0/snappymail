// Package service implements the non-JSON endpoints of RainLoop\ServiceActions
// (?/Ping, ?/AppData, ?/Lang, ?/Css, ?/Raw, ?/Upload, …).
//
// Step 2 needs the Ping endpoint so the router can be verified end to end,
// step 10 adds AppData/AdminAppData, step 11 adds Lang/Css/Plugins and the
// remaining services are added by steps 12–13.
package service

import (
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Ping is ServiceActions::ServicePing(): a "no-store" text/plain "Pong".
func Ping(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	httpx.NoStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if http.MethodHead != r.Method {
		_, _ = w.Write([]byte("Pong"))
	}

	return true
}

// Register adds the implemented service endpoints to the router.
//
// appData may be nil (a router without the AppData endpoints, which the tests
// of the pipeline use); the later steps pass their own services the same way.
func Register(router *web.Router, appData *AppData) {
	router.Register("Ping", Ping)

	if nil != appData {
		router.Register("AppData", appData.Serve)
		router.Register("AdminAppData", appData.ServeAdmin)
		router.Register("Lang", appData.Lang)
		router.Register("Css", appData.Css)
		router.Register("Plugins", appData.Plugins)
	}
}
