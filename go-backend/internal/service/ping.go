// Package service implements the non-JSON endpoints of
// RainLoop\ServiceActions (?/Ping, ?/Lang, ?/Css, ?/Raw, ?/Upload, …).
//
// Step 2 only needs the Ping endpoint so the router can be verified end to
// end; the remaining services are added by steps 10–13.
package service

import (
	"io"
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
		_, _ = io.WriteString(w, "Pong")
	}

	return true
}

// Register adds the step-2 endpoint stubs to the router.
func Register(router *web.Router) {
	router.Register("Ping", Ping)
}
