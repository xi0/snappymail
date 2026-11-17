package web

import (
	"net/http"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

// setHeaders writes the headers Service::RunResult() sets before anything else
// (Server, Referrer-Policy, X-Content-Type-Options, CSP, X-XSS-Protection),
// plus the HSTS header include.php sends for secure requests.
func (rt *Router) setHeaders(w http.ResponseWriter, r *http.Request) {
	header := w.Header()

	if signature := strings.TrimSpace(rt.config.ServerSignature); "" != signature {
		header.Set("Server", signature)
	}

	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")

	cspFor(rt.config, rt.paths.Path()).SetHeaders(w)

	xssProtection := strings.TrimSpace(rt.config.XSSProtectionHeader)
	if "" == xssProtection {
		xssProtection = "1; mode=block"
	}
	header.Set("X-XSS-Protection", xssProtection)

	if httpx.IsSecure(r) {
		header.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

// httpsRedirectTarget is the security.force_https redirect target: https:// +
// the host without "www." + REQUEST_URI, exactly like
// \MailSo\Base\Http::Location('https://'.$oHttp->GetHost(false).$oHttp->GetUrl()).
func httpsRedirectTarget(r *http.Request) string {
	return "https://" + httpx.Host(r, true, false) + httpx.RequestURI(r)
}

// Output compression (webmail.compress_output) lives in compress.go, together
// with the brotli/gzip negotiation.
