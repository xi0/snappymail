// Package l10n ports the language discovery of SnappyMail: the
// SnappyMail\L10n::getLanguages()/validLanguage() pair and the Accept-Language
// detection of \RainLoop\Actions\Localization::detectClientLanguage().
//
// The list of available languages is derived from the install tree rather than
// built in, because the frontend and the PHP backend share
// app/localization/<lang>/{user,admin,static}.json. A language is available
// when the file of the requested area exists:
//
//	<b>app/localization/*</b> dirs holding <b>user.json</b>  -> webmail languages
//	<b>app/localization/*</b> dirs holding <b>admin.json</b> -> admin panel languages
//
// Step 10 (internal/service, AppData) only needs the list and the validation;
// step 11 adds the en -> base -> exact merge of L10n::load() and the
// compileLanguage() JavaScript on top of this package.
package l10n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The file names L10n::getLanguages() probes for.
const (
	// userFile is <lang>/user.json, the webmail texts.
	userFile = "user.json"
	// adminFile is <lang>/admin.json, the admin panel texts.
	adminFile = "admin.json"
)

// acceptLanguagePattern is the '/([a-z]{1,8}(?:-[a-z]{1,8})?)(?:;q=([0-9.]+))?/'
// pattern of detectClientLanguage().
var acceptLanguagePattern = regexp.MustCompile(`([a-z]{1,8}(?:-[a-z]{1,8})?)(?:;q=([0-9.]+))?`)

// Catalog resolves the available languages of one install tree.
//
// The directory scan mirrors the static $aCache of L10n::getLanguages(): the
// list is computed once per area and shared by every request.
type Catalog struct {
	// localizationDir is APP_VERSION_ROOT_PATH . 'app/localization'.
	localizationDir string

	mu     sync.Mutex
	lists  map[bool][]string
	loaded map[bool]bool
}

// NewCatalog builds the catalog for a version root (APP_VERSION_ROOT_PATH,
// with or without a trailing separator).
func NewCatalog(versionRoot string) *Catalog {
	return &Catalog{
		localizationDir: filepath.Join(strings.TrimRight(versionRoot, `/\`), "app", "localization"),
		lists:           map[bool][]string{},
		loaded:          map[bool]bool{},
	}
}

// Languages is L10n::getLanguages(): the language codes of an area, sorted
// ascending. The result must not be modified by the caller.
func (c *Catalog) Languages(admin bool) []string {
	if nil == c {
		return []string{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded[admin] {
		return c.lists[admin]
	}

	list := c.scan(admin)
	c.lists[admin] = list
	c.loaded[admin] = true

	return list
}

// scan is the glob()/is_file() loop of getLanguages().
func (c *Catalog) scan(admin bool) []string {
	file := userFile
	if admin {
		file = adminFile
	}

	entries, err := os.ReadDir(c.localizationDir)
	if nil != err {
		return []string{}
	}

	list := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		// glob('*') skips dotfiles.
		if '.' == name[0] {
			continue
		}
		if !entry.IsDir() {
			continue
		}
		info, statErr := os.Stat(filepath.Join(c.localizationDir, name, file))
		if nil == statErr && info.Mode().IsRegular() {
			list = append(list, name)
		}
	}

	sort.Strings(list)

	return list
}

// ValidLanguage is L10n::validLanguage() for one area: the language code as
// stored, or "" when the language is not available.
func (c *Catalog) ValidLanguage(language string, admin bool) string {
	return ValidLanguage(c.Languages(admin), language)
}

// ValidLanguage is L10n::validLanguage() for a known list: "sv_SE" and "sv-SE"
// both resolve to "sv-SE" when available, "sv-SE" falls back to "sv" and an
// unknown language yields "".
func ValidLanguage(languages []string, language string) string {
	language = strings.ReplaceAll(language, "_", "-")

	if !contains(languages, language) && strings.Contains(language, "-") {
		// PHP: strtok($sLanguage, '-') skips leading delimiters and stops at
		// the next one; a language that starts with "-" therefore keeps its
		// first label
		if trimmed := strings.TrimLeft(language, "-"); "" != trimmed {
			if index := strings.IndexByte(trimmed, '-'); 0 <= index {
				language = trimmed[:index]
			} else {
				language = trimmed
			}
		} else {
			language = ""
		}
	}

	if contains(languages, language) {
		return language
	}
	return ""
}

// contains is PHP's in_array() for strings (case sensitive).
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// AcceptLanguage is the "$sAcceptLang" parsing of detectClientLanguage(): the
// lowercased language codes of the Accept-Language header in descending q=
// order.
//
// PHP builds the list with preg_match_all() + array_combine() and then
// arsort(SORT_NUMERIC), which keeps dropping/keeping the *first* position of a
// repeated code but takes its last q value; PHP 8's sort is stable, so equal
// weights keep the header order.
func AcceptLanguage(header string) []string {
	header = strings.ToLower(header)
	if "" == header {
		return nil
	}

	type weighted struct {
		code   string
		weight float64
	}

	var order []weighted
	positions := map[string]int{}

	for _, match := range acceptLanguagePattern.FindAllStringSubmatch(header, -1) {
		code := match[1]
		weight := 1.0

		if "" != match[2] {
			// $aLangs[$n] = $v ? $v : 1; the numeric sort casts afterwards.
			if parsed, err := strconv.ParseFloat(match[2], 64); nil == err {
				weight = parsed
			} else {
				weight = 0
			}
		}

		if at, ok := positions[code]; ok {
			order[at].weight = weight
			continue
		}
		positions[code] = len(order)
		order = append(order, weighted{code: code, weight: weight})
	}

	sort.SliceStable(order, func(left, right int) bool {
		return order[left].weight > order[right].weight
	})

	codes := make([]string, 0, len(order))
	for _, item := range order {
		codes = append(codes, item.code)
	}

	return codes
}
