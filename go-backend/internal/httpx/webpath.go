package httpx

import (
	"os"
	"path"
	"strconv"
	"strings"
)

// WebPaths mirrors the URL helpers of \RainLoop\Utils.
//
// The Go backend is the front controller of the whole install tree, so unlike
// PHP there is no SCRIPT_NAME to derive webmail.app_path from: an empty AppPath
// simply means "the application is served from the root".
type WebPaths struct {
	// AppPath is webmail.app_path ("/mail" when SnappyMail lives in a
	// subdirectory).
	AppPath string
	// Version is APP_VERSION, the directory below snappymail/v/.
	Version string
	// VersionRoot is APP_VERSION_ROOT_PATH. It is only used to look up the
	// modification time for the cache busting "?v=" suffix.
	VersionRoot string
}

// Path is \RainLoop\Utils::WebPath(): the URL path of the application root,
// always with a trailing slash.
func (p WebPaths) Path() string {
	return strings.TrimRight(p.AppPath, "/") + "/"
}

// VersionPath is \RainLoop\Utils::WebVersionPath().
func (p WebPaths) VersionPath() string {
	return p.Path() + "snappymail/v/" + p.Version + "/"
}

// StaticRoot is the URL prefix of the static folder
// (WebVersionPath() . 'static/').
func (p WebPaths) StaticRoot() string {
	return p.VersionPath() + "static/"
}

// ThemeRoot is the URL prefix of the themes folder
// (WebVersionPath() . 'themes/').
func (p WebPaths) ThemeRoot() string {
	return p.VersionPath() + "themes/"
}

// StaticFile is the filesystem path of a static asset: APP_VERSION_ROOT_PATH
// . 'static/' . rel.
func (p WebPaths) StaticFile(rel string) string {
	return filesystemRoot(p.VersionRoot) + "static" + string(os.PathSeparator) +
		strings.ReplaceAll(rel, "/", string(os.PathSeparator))
}

// VersionFile is the filesystem path of any file below the version root:
// APP_VERSION_ROOT_PATH . rel (rel uses forward slashes).
func (p WebPaths) VersionFile(rel string) string {
	relative := path.Clean("/" + rel)
	return filesystemRoot(p.VersionRoot) + strings.TrimPrefix(
		strings.ReplaceAll(relative, "/", string(os.PathSeparator)), string(os.PathSeparator))
}

// StaticPath is \RainLoop\Utils::WebStaticPath(): the public URL of a static
// asset, with a cache busting "?v=<mtime>" suffix when the file exists.
func (p WebPaths) StaticPath(rel string) string {
	result := p.StaticRoot() + rel

	if "" != rel {
		if info, err := os.Stat(p.StaticFile(rel)); nil == err && !info.IsDir() {
			result += "?v=" + strconv.FormatInt(info.ModTime().Unix(), 10)
		}
	}

	return result
}

// filesystemRoot returns root with exactly one trailing separator.
func filesystemRoot(root string) string {
	separator := string(os.PathSeparator)
	if "" == root {
		return ""
	}
	return strings.TrimRight(root, "/\\") + separator
}
