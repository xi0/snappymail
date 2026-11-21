package web

import (
	"net/http"
	"net/url"
	"strings"
)

// directive is one Content-Security-Policy directive with its sources. A slice
// is used instead of a map because the header layout (directive order) is part
// of the observable output.
type directive struct {
	name    string
	sources []string
}

// cspDefaultDirectives is the default directive table of \SnappyMail\HTTP\CSP.
var cspDefaultDirectives = []directive{
	{"base-uri", []string{"'self'"}},
	{"default-src", []string{"'self'", "data:"}},
	// Knockout.js requires eval() for observable binding purposes
	{"script-src", []string{"'self'", "'unsafe-eval'"}},
	{"img-src", []string{"'self'", "data:"}},
	{"media-src", []string{"'self'", "data:"}},
	{"style-src", []string{"'self'", "'unsafe-inline'"}},
	{"connect-src", []string{"'self'", "data:", "keys.openpgp.org"}},
}

// directiveReportURI is deprecated in the CSP3 draft but still emitted by PHP
// when CSP reporting is enabled.
const directiveReportURI = "report-uri"

// CSP is a port of \SnappyMail\HTTP\CSP.
type CSP struct {
	directives []directive
	// Report adds the report-uri directive.
	Report bool
	// ReportOnly sends Content-Security-Policy-Report-Only instead of
	// Content-Security-Policy.
	ReportOnly bool
	// reportURI is the value of the report-uri directive.
	reportURI string
}

// NewCSP builds the CSP like (new \SnappyMail\HTTP\CSP($default)).
//
// defaultPolicy is security.content_security_policy: ";" separated directives
// whose sources are merged into the defaults. reportURI is where reports go
// when Report/ReportOnly is set.
func NewCSP(defaultPolicy, reportURI string) *CSP {
	csp := &CSP{reportURI: reportURI}

	for _, entry := range cspDefaultDirectives {
		csp.directives = append(csp.directives, directive{
			name:    entry.name,
			sources: append([]string(nil), entry.sources...),
		})
	}

	for _, entry := range strings.Split(defaultPolicy, ";") {
		sources := strings.Fields(strings.TrimSpace(entry))
		if 0 == len(sources) {
			continue
		}

		// PHP creates the directive even when the policy only names it.
		csp.ensure(sources[0])
		for _, source := range sources[1:] {
			csp.Add(sources[0], source)
		}
	}

	return csp
}

// ensure creates a directive without sources when it does not exist yet.
func (c *CSP) ensure(name string) {
	for index := range c.directives {
		if c.directives[index].name == name {
			return
		}
	}

	c.directives = append(c.directives, directive{name: name})
}

// Add appends a source to a directive, creating the directive when needed.
func (c *CSP) Add(name, source string) {
	for index := range c.directives {
		if c.directives[index].name == name {
			c.directives[index].sources = append(c.directives[index].sources, source)
			return
		}
	}

	c.directives = append(c.directives, directive{name: name, sources: []string{source}})
}

// Get returns the sources of a directive (empty when unknown).
func (c *CSP) Get(name string) []string {
	for index := range c.directives {
		if c.directives[index].name == name {
			return append([]string(nil), c.directives[index].sources...)
		}
	}
	return nil
}

// String renders the header value.
func (c *CSP) String() string {
	// report-uri first goes away: PHP unsets it before rendering so it always
	// ends up last (and is not added at all when reporting is off).
	effective := make([]directive, 0, len(c.directives)+1)
	for _, entry := range c.directives {
		if directiveReportURI == entry.name {
			continue
		}
		effective = append(effective, entry)
	}

	if c.Report || c.ReportOnly {
		effective = append(effective, directive{directiveReportURI, []string{c.reportURI}})
	}

	params := make([]string, 0, len(effective))
	for _, entry := range effective {
		params = append(params, entry.name+" "+strings.Join(uniqueStrings(entry.sources), " "))
	}

	return strings.Join(params, "; ")
}

// SetHeaders writes the CSP and the matching X-Frame-Options header.
func (c *CSP) SetHeaders(w http.ResponseWriter) {
	value := c.String()
	if c.ReportOnly {
		w.Header().Set("Content-Security-Policy-Report-Only", value)
	} else {
		w.Header().Set("Content-Security-Policy", value)
	}

	frameAncestors := c.Get("frame-ancestors")
	if 0 == len(frameAncestors) || containsString(frameAncestors, "none") {
		w.Header().Set("X-Frame-Options", "DENY")
	}
}

// cspFor builds the CSP of a request exactly like \RainLoop\Api::getCSP().
// webPath is the application URL path (RainLoop\Utils::WebPath()).
func cspFor(config Config, webPath string) *CSP {
	csp := NewCSP(strings.TrimSpace(config.ContentSecurityPolicy), webPath+"?/CspReport")
	csp.Report = config.CSPReport
	// debug.enable makes the whole policy report-only (SNAPPYMAIL_DEV is not
	// ported, the Go backend always reports as a release build).
	csp.ReportOnly = config.Debug

	// Allow favicon host, if present.
	if faviconURL, err := url.Parse(strings.TrimSpace(config.FaviconURL)); nil == err && "" != faviconURL.Host {
		csp.Add("img-src", faviconURL.Host)
	}

	// Allow https: due to remote images in e-mails or use proxy.
	if !config.LocalProxyExternalImages {
		csp.Add("img-src", "https:")
		csp.Add("img-src", "http:")
	}

	return csp
}

// RequestCSP is Api::getCSP($nonce): the CSP of a request, with the script
// nonce added to script-src when nonce is not empty.
//
// The index page uses it to re-emit the header once the nonce is known (see
// internal/pages). The plugin hook main.content-security-policy is wired in
// step 33.
func RequestCSP(config Config, webPath, nonce string) *CSP {
	csp := cspFor(config, webPath)
	if "" != nonce {
		csp.Add("script-src", "'nonce-"+nonce+"'")
	}
	return csp
}

// uniqueStrings drops duplicates while keeping the first occurrence order
// (PHP: array_unique()).
func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !containsString(result, value) {
			result = append(result, value)
		}
	}
	return result
}

// containsString reports whether values contains value.
func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
