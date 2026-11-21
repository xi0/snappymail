package web

import (
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// serverSoftware replaces $_SERVER['SERVER_SOFTWARE'] in the request log line
// and sapi replaces PHP_SAPI.
const (
	serverSoftware = "snappymail-go"
	sapi           = runtime.GOOS + "/" + runtime.GOARCH
)

// Route is the parsed query-string path vector of
// RainLoop\Service::RunResult().
type Route struct {
	// Query is the vector source string ("Json/0").
	Query string
	// Paths is the path vector (PHP: explode('/', $sQuery)) with the admin
	// panel key already shifted off.
	Paths []string
	// Admin reports whether the admin panel was addressed, either by
	// admin_panel.host or by the first path element.
	Admin bool
	// Name is the service name: Paths[0] without its "@extra" suffix.
	Name string
	// Extra is the "@extra" suffix of Paths[0].
	Extra string
}

// ServiceFunc handles a "?/Name" request. It writes the response and reports
// whether it handled the request; an unhandled request falls through to the
// index page, exactly like Service::RunResult() does when there is no
// Service<Name> method.
type ServiceFunc func(w http.ResponseWriter, r *http.Request, route *Route) bool

// IndexFunc renders the application shell (app/templates/Index.html).
type IndexFunc func(w http.ResponseWriter, r *http.Request, route *Route)

// Router is the port of the first half of RainLoop\Service::RunResult(): the
// security headers, the force_https redirect, output compression, the
// query-string path vector, the admin panel and Sec-Fetch gates and the
// dispatch to the Service* endpoints.
type Router struct {
	config Config
	log    *logx.Logger
	paths  httpx.WebPaths
	static *Static
	index  IndexFunc

	services map[string]ServiceFunc
}

// NewRouter builds the request pipeline.
//
// A nil logger disables request logging and a nil index falls back to
// DefaultIndex (the step-2 placeholder for the pages package).
func NewRouter(config Config, logger *logx.Logger, paths httpx.WebPaths, index IndexFunc) *Router {
	if nil == index {
		index = DefaultIndex
	}

	return &Router{
		config:   config,
		log:      logger,
		paths:    paths,
		static:   &Static{Root: paths.VersionRoot, Prefix: paths.VersionPath()},
		index:    index,
		services: map[string]ServiceFunc{},
	}
}

// Register adds a "?/Name" endpoint (PHP: the Service<Name> method of
// RainLoop\ServiceActions).
func (rt *Router) Register(name string, service ServiceFunc) {
	rt.services[name] = service
}

// Config returns the pipeline configuration.
func (rt *Router) Config() Config { return rt.config }

// Paths returns the install tree URL helpers.
func (rt *Router) Paths() httpx.WebPaths { return rt.paths }

// ServeHTTP runs the request pipeline in the order of Service::RunResult().
func (rt *Router) ServeHTTP(writer http.ResponseWriter, r *http.Request) {
	rt.setHeaders(writer, r)
	rt.logRequest(r)

	// security.force_https
	if rt.config.ForceHTTPS && !httpx.IsSecure(r) {
		httpx.Location(writer, httpsRedirectTarget(r), http.StatusFound)
		return
	}

	// webmail.compress_output
	writer, finish := rt.compress(writer, r)
	defer finish()

	// Static assets are served by the webserver in the PHP setup, so they are
	// answered before any path vector parsing.
	if nil != rt.static && rt.static.Serve(writer, r) {
		return
	}

	route := rt.parseRoute(r)

	// Http::ServerNoCache() for POST, before the gates write their own body.
	if httpx.IsPost(r) {
		httpx.NoStore(writer)
	}

	// security.allow_admin_panel
	if route.Admin && !rt.config.AllowAdminPanel {
		rt.writeError(writer, r, http.StatusForbidden, "Access Denied.",
			"Access to the SnappyMail Admin Panel is not allowed!")
		return
	}

	if rt.isServicePath(route) {
		// security.secfetch_allow, "mailto" is exempt like in PHP.
		if !strings.EqualFold(route.Name, "mailto") && !SecFetchMatchAnyRule(r, rt.config.SecFetchAllow) {
			rt.writeError(writer, r, http.StatusForbidden, "Access Denied.", secFetchReport(r))
			return
		}

		if service, ok := rt.services[route.Name]; ok && service(writer, r, route) {
			return
		}

		// No Service<Name> method (and, once plugins exist, no plugin
		// additional part): the index page is served instead.
	}

	if !rt.isFrontController(r.URL.Path) && forbiddenPath(r.URL.Path) {
		http.NotFound(writer, r)
		return
	}

	// The index page is never cached (issue 1024).
	httpx.NoStore(writer)

	rt.index(writer, r, route)
}

// parseRoute reproduces the path vector part of Service::RunResult(): explode
// the query path, then shift the admin panel key off the front unless
// admin_panel.host decides the admin area.
func (rt *Router) parseRoute(r *http.Request) *Route {
	route := &Route{Query: httpx.QueryPath(r)}
	route.Paths = strings.Split(route.Query, "/")

	if adminHost := strings.TrimSpace(rt.config.AdminHost); "" != adminHost {
		route.Admin = strings.EqualFold(adminHost, httpx.Host(r, false, false))
	} else {
		adminKey := rt.config.AdminKey
		if "" == adminKey {
			adminKey = "admin"
		}
		if 0 < len(route.Paths) && adminKey == route.Paths[0] {
			route.Admin = true
			route.Paths = route.Paths[1:]
		}
	}

	if 0 < len(route.Paths) {
		route.Name, route.Extra = httpx.SplitSegment(route.Paths[0])
	}

	return route
}

// isServicePath reports whether the path vector addresses a service at all:
// PHP serves the index page when the vector is empty or starts with "index".
func (rt *Router) isServicePath(route *Route) bool {
	if 0 == len(route.Paths) || "" == route.Paths[0] {
		return false
	}
	return !strings.EqualFold(route.Paths[0], "index")
}

// isFrontController reports whether the URL path addresses index.php in the
// install root, which is the application itself and must never 404.
func (rt *Router) isFrontController(urlPath string) bool {
	trimmed := strings.Trim(urlPath, "/")

	if prefix := strings.Trim(rt.paths.Path(), "/"); "" != prefix {
		trimmed = strings.TrimPrefix(trimmed, prefix+"/")
	}

	return "index.php" == trimmed ||
		strings.HasPrefix(strings.ToLower(trimmed), "index.php/")
}

// logRequest writes the per-request line the Actions constructor logs in PHP:
// [SM:<version>][IP:<client>][PID:<pid>][<SERVER_SOFTWARE>][<SAPI>][<METHOD>
// <scheme>://<host><REQUEST_URI>].
func (rt *Router) logRequest(r *http.Request) {
	if nil == rt.log || !rt.log.Enabled() {
		return
	}

	rt.log.Infof("[SM:%s][IP:%s][PID:%d][%s][%s][%s %s://%s%s]",
		rt.paths.Version,
		httpx.ClientIP(r, rt.config.TrustProxyClientIP),
		os.Getpid(),
		serverSoftware,
		sapi,
		httpx.Method(r),
		httpx.Scheme(r),
		httpx.Host(r, false, false),
		httpx.RequestURI(r),
	)
}

// DefaultIndex is the placeholder index page for routers built without the
// pages renderer (the router tests). main wires pages.Renderer.Index instead.
func DefaultIndex(w http.ResponseWriter, r *http.Request, route *Route) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "<!DOCTYPE html>\n"+
		"<html lang=\"en\" dir=\"ltr\">\n"+
		"<head>\n"+
		"<meta charset=\"utf-8\">\n"+
		"<title>SnappyMail</title>\n"+
		"</head>\n"+
		"<body>\n"+
		"<p>SnappyMail Go backend: the index template is rendered in step 11.</p>\n"+
		"</body>\n"+
		"</html>\n")
}
