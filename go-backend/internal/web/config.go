// Package web implements the routing and HTTP surface of the SnappyMail
// backend: the query-string path vector of RainLoop\Service::RunResult(), the
// request pipeline (security headers, force_https, output compression, cache
// headers), the Sec-Fetch gate and the static asset whitelist.
package web

// Config carries the configs/application.ini values the request pipeline needs.
//
// Step 3 adds the config package that loads application.ini; it maps the loaded
// values onto this struct. Until then main.go uses DefaultConfig(), which holds
// the RainLoop\Config\Application::defaultValues() defaults, so the pipeline
// behaves like a fresh PHP install.
type Config struct {
	// ServerSignature is security.custom_server_signature. When empty no
	// Server header is sent at all (PHP: strlen() check in RunResult()).
	ServerSignature string

	// XSSProtectionHeader is security.x_xss_protection_header, with PHP's
	// "1; mode=block" fallback applied by the pipeline when it is empty.
	XSSProtectionHeader string

	// ForceHTTPS is security.force_https: redirect http:// to https://.
	ForceHTTPS bool

	// AllowAdminPanel is security.allow_admin_panel.
	AllowAdminPanel bool

	// AdminHost is admin_panel.host. When set, the Host header decides whether
	// a request addresses the admin panel, else the first path element has to
	// equal AdminKey.
	AdminHost string

	// AdminKey is admin_panel.key (PHP default "admin").
	AdminKey string

	// CompressOutput is webmail.compress_output: brotli or gzip compress the
	// output (see compress.go).
	CompressOutput bool

	// ContentSecurityPolicy is security.content_security_policy: extra
	// directives merged into the default CSP.
	ContentSecurityPolicy string

	// CSPReport is security.csp_report: add a report-uri directive.
	CSPReport bool

	// SecFetchAllow is security.secfetch_allow: extra allowed Sec-Fetch
	// combinations, separated by ";" (the site=same-origin;site=none rules are
	// always allowed).
	SecFetchAllow string

	// TrustProxyClientIP is labs.http_client_ip_check_proxy: honour the
	// Client-IP / X-Forwarded-For headers for the client IP.
	TrustProxyClientIP bool

	// FaviconURL is webmail.favicon_url. Only its host is used, to allow the
	// favicon in the CSP img-src directive.
	FaviconURL string

	// LocalProxyExternalImages is labs.use_local_proxy_for_external_images.
	// When it is disabled the CSP has to allow remote images directly.
	LocalProxyExternalImages bool

	// Debug is debug.enable: the CSP is sent as report-only.
	Debug bool
}

// DefaultConfig returns the defaults the pipeline needs from
// RainLoop\Config\Application::defaultValues().
func DefaultConfig() Config {
	return Config{
		ServerSignature:          "SnappyMail",
		XSSProtectionHeader:      "1; mode=block",
		ForceHTTPS:               false,
		AllowAdminPanel:          true,
		AdminHost:                "",
		AdminKey:                 "admin",
		CompressOutput:           false,
		ContentSecurityPolicy:    "",
		CSPReport:                false,
		SecFetchAllow:            "",
		TrustProxyClientIP:       false,
		FaviconURL:               "",
		LocalProxyExternalImages: true,
		Debug:                    false,
	}
}
