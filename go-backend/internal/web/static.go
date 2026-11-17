package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// staticMaxAge is the "A15768000" (six months) freshness of the Apache
	// configuration in .htaccess.
	staticMaxAge = 15768000 * time.Second
	// serviceWorkerAllowed mirrors "Header set Service-Worker-Allowed" of the
	// root .htaccess, which the service worker needs to control the whole app.
	serviceWorkerAllowed = "/"
)

// jsAssets are the only JavaScript files the frontend loads directly; the
// minified variants live in static/js/min/.
var jsAssets = map[string]bool{
	"boot.js":          true,
	"libs.js":          true,
	"app.js":           true,
	"admin.js":         true,
	"sieve.js":         true,
	"openpgp.js":       true,
	"serviceworker.js": true,
}

// cssAssets are the only stylesheets of static/css/.
var cssAssets = map[string]bool{
	"boot.css":      true,
	"boot.min.css":  true,
	"app.css":       true,
	"app.min.css":   true,
	"admin.css":     true,
	"admin.min.css": true,
}

var (
	imageExtensions = []string{".png", ".gif", ".jpg", ".jpeg", ".svg", ".webp", ".ico", ".bmp", ".avif"}
	fontExtensions  = []string{".woff", ".woff2", ".ttf", ".otf", ".eot"}
	soundExtensions = []string{".mp3", ".ogg", ".wav"}
)

// staticContentTypes is the explicit Content-Type map: Go's mime package
// returns "text/javascript" for .js on many systems, while the Apache setup
// (static/apache.htaccess ForceType) serves "application/javascript".
var staticContentTypes = map[string]string{
	".js":    "application/javascript",
	".css":   "text/css",
	".less":  "text/css",
	".json":  "application/json",
	".png":   "image/png",
	".gif":   "image/gif",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".svg":   "image/svg+xml",
	".webp":  "image/webp",
	".ico":   "image/vnd.microsoft.icon",
	".bmp":   "image/bmp",
	".avif":  "image/avif",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/opentype",
	".eot":   "application/vnd.ms-fontobject",
	".mp3":   "audio/mpeg",
	".ogg":   "audio/ogg",
	".wav":   "audio/wav",
}

// forbiddenTopLevel are the install tree directories that must never be
// served. SPEC.md: "Files not consumed by the frontend should not be available
// in the Go backend".
var forbiddenTopLevel = map[string]bool{
	"app":          true,
	"build":        true,
	"cli":          true,
	"data":         true,
	"dev":          true,
	"examples":     true,
	"fail2ban":     true,
	"integrations": true,
	"node_modules": true,
	"plugins":      true,
	"snappymail":   true,
	"tasks":        true,
	"test":         true,
	"tests":        true,
	"vendors":      true,
}

// Static serves the on-disk frontend assets of the version root.
//
// The Apache setup serves everything below APP_VERSION_ROOT_PATH; the Go
// backend has to restrict itself to the files the frontend actually consumes,
// which is the whitelist implemented here.
type Static struct {
	// Root is APP_VERSION_ROOT_PATH.
	Root string
	// Prefix is the URL prefix of the version root
	// (httpx.WebPaths.VersionPath()).
	Prefix string
}

// Serve answers a static asset request. It returns false when the request does
// not address the version root at all (so the caller keeps going), and true
// after writing a response, including the 404 for non-whitelisted paths.
func (s *Static) Serve(w http.ResponseWriter, r *http.Request) bool {
	if nil == s || "" == s.Root || "" == s.Prefix || nil == r || nil == r.URL {
		return false
	}

	if !strings.HasPrefix(r.URL.Path, s.Prefix) {
		return false
	}

	if http.MethodGet != r.Method && http.MethodHead != r.Method {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}

	relative := r.URL.Path[len(s.Prefix):]
	if !allowedStatic(relative) {
		// Not consumed by the frontend: app/, app/localization/, plugins/,
		// *.php, .htaccess, ... stay unavailable.
		http.NotFound(w, r)
		return true
	}

	file := s.file(relative)

	info, err := os.Stat(file)
	if nil != err || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return true
	}

	handle, err := os.Open(file)
	if nil != err {
		http.NotFound(w, r)
		return true
	}
	defer func() {
		_ = handle.Close()
	}()

	header := w.Header()
	header.Set("Content-Type", staticContentType(relative))
	header.Set("Cache-Control", "max-age="+strconv.FormatInt(int64(staticMaxAge/time.Second), 10))
	header.Set("Expires", time.Now().Add(staticMaxAge).UTC().Format(http.TimeFormat))
	header.Set("Service-Worker-Allowed", serviceWorkerAllowed)
	header.Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size()))

	// ServeContent adds Accept-Ranges/Last-Modified and handles Range,
	// If-Modified-Since, If-None-Match and HEAD.
	http.ServeContent(w, r, info.Name(), info.ModTime(), handle)

	return true
}

// file resolves a whitelisted relative name below the version root.
func (s *Static) file(relative string) string {
	root := strings.TrimRight(s.Root, "/\\")
	return root + string(filepath.Separator) + filepath.FromSlash(relative)
}

// allowedStatic reports whether a path below the version root may be served.
func allowedStatic(relative string) bool {
	if "" == relative || strings.HasPrefix(relative, "/") || strings.ContainsAny(relative, "\\\x00") {
		return false
	}

	segments := strings.Split(relative, "/")
	for _, segment := range segments {
		if !safeSegment(segment) {
			return false
		}
	}

	if "static/favicon.png" == relative || "static/manifest.json" == relative {
		return true
	}

	if "static" == segments[0] {
		return allowedStaticFile(segments)
	}

	if "themes" == segments[0] {
		return allowedThemeFile(segments)
	}

	return false
}

// allowedStaticFile is the whitelist of static/{js,css,images,sounds}.
func allowedStaticFile(segments []string) bool {
	if 3 > len(segments) {
		return false
	}

	rest := segments[2:]

	switch segments[1] {
	case "js":
		if 2 == len(rest) {
			return "min" == rest[0] && strings.HasSuffix(rest[1], ".js")
		}
		return 1 == len(rest) && jsAssets[rest[0]]

	case "css":
		if 2 == len(rest) {
			return "fonts" == rest[0] && hasExtension(rest[1], fontExtensions)
		}
		return 1 == len(rest) && cssAssets[rest[0]]

	case "images":
		return 1 == len(rest) && hasExtension(rest[0], imageExtensions)

	case "sounds":
		return 1 == len(rest) && hasExtension(rest[0], soundExtensions)
	}

	return false
}

// allowedThemeFile is the whitelist of themes/<theme>/{styles.css,styles.less,
// images/*}. LESS is compiled by lessphp; the Go backend serves the file as-is
// (the shipped themes are plain CSS, see SPEC.md).
func allowedThemeFile(segments []string) bool {
	if 4 == len(segments) {
		return "images" == segments[2] && validThemeName(segments[1]) &&
			hasExtension(segments[3], imageExtensions)
	}

	if 3 != len(segments) {
		return false
	}

	if !validThemeName(segments[1]) {
		return false
	}

	return "styles.css" == segments[2] || "styles.less" == segments[2]
}

// validThemeName rejects traversal and keeps the characters themes may use
// (including the "@custom"/"@nextcloud" prefixes).
func validThemeName(name string) bool {
	if "" == name || "." == name || ".." == name {
		return false
	}

	for index := 0; index < len(name); index++ {
		c := name[index]
		isAlnum := ('a' <= c && 'z' >= c) || ('A' <= c && 'Z' >= c) || ('0' <= c && '9' >= c)
		if !isAlnum && '_' != c && '-' != c && '@' != c && '.' != c {
			return false
		}
	}

	return true
}

// safeSegment drops empty segments and dot files/directories.
func safeSegment(segment string) bool {
	return "" != segment && !strings.HasPrefix(segment, ".")
}

// hasExtension reports whether name ends with one of the extensions.
func hasExtension(name string, extensions []string) bool {
	lower := strings.ToLower(name)
	for _, extension := range extensions {
		if strings.HasSuffix(lower, extension) {
			return true
		}
	}
	return false
}

// staticContentType returns the explicit Content-Type of an asset.
func staticContentType(relative string) string {
	extension := strings.ToLower(filepath.Ext(relative))
	if contentType, ok := staticContentTypes[extension]; ok {
		return contentType
	}
	return "application/octet-stream"
}

// forbiddenPath reports whether the URL path addresses something that must not
// be exposed at all (install tree sources, private data, PHP scripts, dot
// files). Such requests are answered with 404 instead of the index page.
func forbiddenPath(urlPath string) bool {
	trimmed := strings.Trim(strings.TrimSpace(urlPath), "/")
	if "" == trimmed {
		return false
	}

	if strings.ContainsAny(trimmed, "\\\x00") {
		return true
	}

	segments := strings.Split(trimmed, "/")

	// index.php in the install root is the application itself (the front
	// controller), not a source file to hide; anything after it is a
	// PATH_INFO path vector.
	if "index.php" == strings.ToLower(segments[0]) {
		return false
	}

	if forbiddenTopLevel[strings.ToLower(segments[0])] {
		return true
	}

	for _, segment := range segments {
		// .htaccess, .htpasswd, .git, .env, ...
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}

	return strings.HasSuffix(strings.ToLower(segments[len(segments)-1]), ".php")
}
