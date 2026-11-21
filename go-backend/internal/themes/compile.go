package themes

// Port of Actions\Themes::compileCss(): the theme stylesheet plus the plugin
// CSS, with the URL bases rewritten so the shipped, "@custom" and "@nextcloud"
// themes keep working from their different locations.
//
// LESS is not ported (see the SPEC dependency notes): a theme that only ships
// styles.less returns ErrLessUnsupported instead of being compiled.

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrLessUnsupported is returned for a theme that needs the LESS compiler.
var ErrLessUnsupported = errors.New("themes: LESS themes are not supported (no LESS compiler ported)")

// PluginCSS is Plugins\Manager::CompileCss().
//
// A nil value means no plugin CSS (plugins.enable = Off).
type PluginCSS interface {
	// CompileCSS returns the plugin stylesheet fragments of the requested
	// scope; less is set to true when a fragment is a .less file.
	CompileCSS(admin bool, less *bool, minified bool) string
}

// urlRewritePattern is the '@(url\(["\']?)(\./)?([a-z]+[^:a-z])@' rewrite of
// compileCss(): relative url()s are prefixed with the theme base URL.
var urlRewritePattern = regexp.MustCompile(`(url\(["']?)(\./)?([a-z]+[^:a-z])`)

// CompileCSS is Actions\Themes::compileCss() (without the LESS path).
func (s Set) CompileCSS(theme string, admin, minified bool, plugins PluginCSS) (string, error) {
	var fragments []string

	base := ""
	stylesheetFile := ""

	if strings.HasSuffix(theme, "@nextcloud") {
		base = strings.TrimRight(s.NextcloudWebRoot, "/") + "/"
		relative := strings.ReplaceAll(theme, "@nextcloud", "/snappymail/style.css")
		stylesheetFile = filepath.Join(s.NextcloudRoot, "themes",
			filepath.FromSlash(strings.TrimPrefix(relative, "/")))
	} else {
		custom := strings.HasSuffix(theme, "@custom")
		if custom {
			theme = strings.TrimSuffix(theme, "@custom")
			base = s.Paths.Path()
		} else {
			base = s.Paths.VersionPath()
		}

		base += "themes/" + theme + "/"

		root := s.VersionRoot
		if custom {
			root = s.IndexRoot
		}
		stylesheetFile = filepath.Join(root, "themes", theme, stylesheet)

		if !isRegularFile(stylesheetFile) {
			stylesheetFile = filepath.Join(root, "themes", theme, lessStylesheet)
			if isRegularFile(stylesheetFile) {
				return "", ErrLessUnsupported
			}
		}
	}

	if isRegularFile(stylesheetFile) {
		content, err := os.ReadFile(stylesheetFile)
		if nil != err {
			return "", err
		}
		fragments = append(fragments, string(content))
	}

	if nil != plugins {
		var pluginLess bool
		css := plugins.CompileCSS(admin, &pluginLess, minified)
		if pluginLess {
			// The plugin part needs the LESS compiler as well.
			return "", ErrLessUnsupported
		}
		fragments = append(fragments, css)
	}

	joined := strings.ReplaceAll(strings.Join(fragments, "\n"), "@{base}", base)

	return urlRewritePattern.ReplaceAllStringFunc(joined, func(match string) string {
		parts := urlRewritePattern.FindStringSubmatch(match)
		return parts[1] + base + parts[3]
	}), nil
}

// isRegularFile mirrors PHP's is_file().
func isRegularFile(name string) bool {
	info, err := os.Stat(name)
	return nil == err && info.Mode().IsRegular()
}
