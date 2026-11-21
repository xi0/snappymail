// Package themes ports the theme discovery of \RainLoop\Actions\Themes:
// GetThemes() and ValidateTheme().
//
// A theme is a directory with a stylesheet, in one of three places:
//
//	<b>snappymail/v/<version>/themes/<name></b>  -> "<name>"        (shipped)
//	<b><index root>/themes/<name></b>            -> "<name>@custom" (operator)
//	<b><nextcloud root>/<name>/snappymail</b>    -> "<name>@nextcloud" (plugin)
//
// The "@custom"/"@nextcloud" suffixes are part of the contract: they are
// written into the settings file and chosen by the frontend, and
// compileCss() (step 11) switches the URL base on them.
package themes

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

// The stylesheet names that make a directory a theme (styles.css wins over
// styles.less, which needs the LESS compiler that is not ported yet).
const (
	// stylesheet is a plain CSS theme.
	stylesheet = "styles.css"
	// lessStylesheet is a LESS theme.
	lessStylesheet = "styles.less"
	// defaultTheme is the ValidateTheme() fallback and the webmail.theme
	// default of application.ini.
	defaultTheme = "Gazellemail"
)

// Set is the install tree part GetThemes() scans.
type Set struct {
	// VersionRoot is APP_VERSION_ROOT_PATH (the shipped themes).
	VersionRoot string
	// IndexRoot is APP_INDEX_ROOT_PATH (operator themes on top of the
	// install tree).
	IndexRoot string
	// NextcloudRoot is OC::$SERVERROOT; empty disables the Nextcloud themes,
	// exactly like the `class_exists('OC', false)` check of GetThemes().
	NextcloudRoot string
	// NextcloudWebRoot is OC::$WEBROOT, the URL base of the Nextcloud themes.
	NextcloudWebRoot string
	// Paths is the install tree URL helpers (WebPath()/WebVersionPath()).
	Paths httpx.WebPaths
}

// List is GetThemes(): the theme names of the install tree, sorted ascending
// and without duplicates.
func (s Set) List() []string {
	var names []string

	names = append(names, scan(filepath.Join(s.VersionRoot, "themes"), "")...)
	names = append(names, scan(filepath.Join(s.IndexRoot, "themes"), "@custom")...)

	if "" != s.NextcloudRoot {
		// Nextcloud themes live at <root>/themes/<name>/snappymail/style.css.
		for _, name := range scanNextcloud(filepath.Join(s.NextcloudRoot, "themes")) {
			names = append(names, name+"@nextcloud")
		}
	}

	// PHP: array_values(array_unique($aCache)) then sort().
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}

	sort.Strings(result)

	return result
}

// Validate is ValidateTheme(): an unknown theme falls back to the configured
// default and finally to Gazellemail.
func (s Set) Validate(theme, fallback string) string {
	list := s.List()

	if !contains(list, theme) {
		theme = fallback
		if !contains(list, theme) {
			theme = defaultTheme
		}
	}

	return theme
}

// scan lists the theme directories of one folder, appending suffix to every
// name. A missing or unreadable folder yields nothing, like opendir() failing
// in PHP.
func scan(dir, suffix string) []string {
	entries, err := os.ReadDir(dir)
	if nil != err {
		return nil
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		// readdir() yields "." and ".."; hidden folders are skipped.
		if '.' == name[0] || !entry.IsDir() {
			continue
		}
		if isThemeDirectory(filepath.Join(dir, name)) {
			names = append(names, name+suffix)
		}
	}

	return names
}

// scanNextcloud lists the Nextcloud theme directories: <dir>/<name>/snappymail/
// style.css.
func scanNextcloud(dir string) []string {
	entries, err := os.ReadDir(dir)
	if nil != err {
		return nil
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if '.' == name[0] || !entry.IsDir() {
			continue
		}
		info, statErr := os.Stat(filepath.Join(dir, name, "snappymail", "style.css"))
		if nil == statErr && info.Mode().IsRegular() {
			names = append(names, name)
		}
	}

	return names
}

// isThemeDirectory reports whether dir holds a stylesheet.
func isThemeDirectory(dir string) bool {
	for _, name := range []string{stylesheet, lessStylesheet} {
		info, err := os.Stat(filepath.Join(dir, name))
		if nil == err && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// contains is PHP's in_array() for strings.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
