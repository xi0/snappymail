package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// WildcardCacheKey is DefaultDomain::CACHE_KEY: the cache entry holding the
// space separated wildcard domain names.
const WildcardCacheKey = "/WildCardDomainsCache/"

// ErrInvalidName is the `!$sRealFileName` early return of DefaultDomain::Save()
// and SaveAlias(): the name cannot be encoded to a file name, or the alias
// target is empty.
var ErrInvalidName = errors.New("domain: invalid domain name")

// Cache is the subset of MailSo\Cache\CacheClient that DefaultDomain uses for
// the wildcard domain list. Step 5 (internal/storage) provides a client that
// satisfies it.
type Cache interface {
	// Get returns the value of key; ok is false when it is not cached.
	Get(key string) (string, bool)
	// Set stores value under key.
	Set(key, value string) error
	// Delete drops key.
	Delete(key string) error
}

// Provider is RainLoop\Providers\Domain\DefaultDomain: the domains/ folder of
// the private data folder.
type Provider struct {
	// dir is sDomainPath: data/_data_/<name>/domains, without a trailing
	// separator.
	dir string
	// cache is the optional CacheClient of the constructor.
	cache Cache
	// defaults seeds new domain configs (the application.ini [ssl] section).
	defaults Defaults
}

// NewProvider builds the provider for a domains folder. A nil cache disables
// the wildcard domain list cache, exactly like DefaultDomain's null $oCacher.
func NewProvider(dir string, cache Cache, defaults Defaults) *Provider {
	return &Provider{
		dir:      strings.TrimRight(strings.TrimSpace(dir), `\/`),
		cache:    cache,
		defaults: defaults,
	}
}

// Dir returns the domains folder of the provider.
func (p *Provider) Dir() string { return p.dir }

// loadOptions are the optional booleans of DefaultDomain::Load().
type loadOptions struct {
	findWithWildCard bool
	checkDisabled    bool
	checkAliases     bool
}

// LoadOption changes one of the optional Load() arguments (PHP defaults:
// no wildcard search, check the disabled list and follow aliases).
type LoadOption func(*loadOptions)

// WithWildcard is Load(..., $bFindWithWildCard = true): fall back to a matching
// wildcard domain when the exact name has no config.
func WithWildcard() LoadOption {
	return func(options *loadOptions) { options.findWithWildCard = true }
}

// WithoutDisabledCheck is Load(..., $bCheckDisabled = false): also load a
// domain that the admin disabled.
func WithoutDisabledCheck() LoadOption {
	return func(options *loadOptions) { options.checkDisabled = false }
}

// WithoutAliasCheck is Load(..., $bCheckAliases = false): do not follow a
// ".alias" file.
func WithoutAliasCheck() LoadOption {
	return func(options *loadOptions) { options.checkAliases = false }
}

// Load is DefaultDomain::Load(): the canonical JSON file, the legacy INI file,
// the alias file and finally the wildcard configs, in that order.
func (p *Provider) Load(name string, options ...LoadOption) *Domain {
	settings := loadOptions{checkDisabled: true, checkAliases: true}
	for _, option := range options {
		option(&settings)
	}

	name = toASCII(name)

	if settings.checkDisabled && contains(p.readDisabled(), name) {
		return nil
	}

	base := filepath.Join(p.dir, encodeFileName(name))

	if file := base + ".json"; isFile(file) {
		return FromArray(name, decodeJSONObject(file), p.defaults)
	}

	if file := base + ".ini"; isFile(file) {
		return FromIniArray(name, parseIniFile(file), p.defaults)
	}

	if settings.checkAliases {
		if file := base + ".alias"; isFile(file) {
			if target := strings.TrimSpace(readFileOrEmpty(file)); "" != target {
				// The target is loaded without the disabled/alias checks, like
				// the recursive Load($sTarget, false, false, false).
				result := p.Load(target, WithoutDisabledCheck(), WithoutAliasCheck())
				if nil != result {
					result.SetAliasName(name)
				}
				return result
			}
		}
	}

	if settings.findWithWildCard {
		if ok, found := ValidateWildcardValues(name, p.wildcardDomainsLine()); ok && "" != found {
			return p.Load(found)
		}
	}

	return nil
}

// Save is DefaultDomain::Save(): remove the old variants, then write the
// canonical JSON file with PHP's pretty printed layout.
func (p *Provider) Save(domain *Domain) error {
	encoded := encodeFileName(domain.Name)
	if "" == encoded {
		return ErrInvalidName
	}

	if _, err := p.Delete(domain.Name); nil != err {
		return err
	}

	content, err := domain.Encode()
	if nil != err {
		return err
	}

	return p.saveFile(encoded+".json", content)
}

// SaveAlias is DefaultDomain::SaveAlias(): store the target domain name in a
// "<name>.alias" file.
func (p *Provider) SaveAlias(name, target string) error {
	target = toASCII(target)

	encoded := encodeFileName(name)
	if "" == encoded || "" == target {
		return ErrInvalidName
	}

	if err := p.saveFile(encoded+".alias", []byte(target)); nil != err {
		return err
	}

	p.forgetWildcardLine()

	return nil
}

// Delete is DefaultDomain::Delete(): remove the JSON, INI and alias files and
// mark the domain disabled when one of them could not be removed.
func (p *Provider) Delete(name string) (bool, error) {
	result := true
	var failure error

	if "" != name {
		base := filepath.Join(p.dir, encodeFileName(name))

		// PHP chains the unlink() calls with &&, so the first failure stops
		// the chain.
		for _, extension := range []string{".json", ".ini", ".alias"} {
			file := base + extension
			_, statErr := os.Lstat(file)
			if nil != statErr {
				continue
			}
			if err := os.Remove(file); nil != err {
				result = false
				if nil == failure {
					failure = fmt.Errorf("delete %s: %w", file, err)
				}
				break
			}
		}

		if _, err := p.Disable(name, !result); nil != err && nil == failure {
			failure = err
		}

		p.forgetWildcardLine()
	}

	return result, failure
}

// Disable is DefaultDomain::Disable(): add or remove the name in the "disabled"
// file (newline separated).
//
// The boolean result compares the re-read file with the in-memory array
// including the array keys PHP keeps through array_filter(), so removing a name
// that was not the first entry reports false even though the file is correct.
func (p *Provider) Disable(name string, disable bool) (bool, error) {
	name = toASCII(name)
	if "" == name {
		return false, nil
	}

	entries := make([]disabledEntry, 0, 8)
	for index, existing := range p.readDisabled() {
		entries = append(entries, disabledEntry{key: index, name: existing})
	}

	if disable {
		entries = append(entries, disabledEntry{key: len(entries), name: name})
	} else {
		filtered := make([]disabledEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.name != name {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}

	// array_unique(): the first occurrence of a name wins and keeps its key.
	seen := make(map[string]struct{}, len(entries))
	unique := make([]disabledEntry, 0, len(entries))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry.name]; ok {
			continue
		}
		seen[entry.name] = struct{}{}
		unique = append(unique, entry)
		names = append(names, entry.name)
	}

	if err := p.saveFile("disabled", []byte(strings.Join(names, "\n"))); nil != err {
		return false, err
	}

	// `return $this->getDisabled() === $aResult;`
	after := p.readDisabled()
	if len(after) != len(unique) {
		return false, nil
	}
	for index, entry := range unique {
		if entry.key != index || after[index] != entry.name {
			return false, nil
		}
	}

	return true, nil
}

// disabledEntry is one name of the disabled list with the array key PHP keeps.
type disabledEntry struct {
	key  int
	name string
}

// ListEntry is one element of DefaultDomain::GetList().
type ListEntry struct {
	// Name is the decoded domain name ("*" for the default domain).
	Name string
	// Disabled reports whether the name is in the disabled list.
	Disabled bool
	// Alias reports whether the entry is a ".alias" file.
	Alias bool
}

// GetList is DefaultDomain::GetList(): normal domains sorted ascending, then
// the aliases sorted ascending, then the wildcard domains sorted descending.
func (p *Provider) GetList(includeAliases bool) []ListEntry {
	entries, err := os.ReadDir(p.dir)
	if nil != err {
		// PHP's scandir() fails as well; the caller sees an empty list instead
		// of a fatal.
		return nil
	}

	disabled := p.readDisabled()

	var result, aliases, wildcards []ListEntry

	for _, entry := range entries {
		file := entry.Name()

		alias := ".alias" == filepath.Ext(file)
		if !alias && ".json" != filepath.Ext(file) && ".ini" != filepath.Ext(file) {
			continue
		}

		name := decodeFileName(stripDomainExtension(file))
		listEntry := ListEntry{
			Name:     name,
			Disabled: contains(disabled, name),
			Alias:    alias,
		}

		switch {
		case alias:
			if includeAliases {
				aliases = append(aliases, listEntry)
			}
		case strings.Contains(listEntry.Name, "*"):
			wildcards = append(wildcards, listEntry)
		default:
			result = append(result, listEntry)
		}
	}

	sortBy(result, false)
	sortBy(aliases, false)
	sortBy(wildcards, true)

	return mergeEntries(result, aliases, wildcards)
}

// sortBy sorts entries by name; descending reverses the order (krsort).
func sortBy(entries []ListEntry, descending bool) {
	sort.SliceStable(entries, func(left, right int) bool {
		if descending {
			return entries[right].Name < entries[left].Name
		}
		return entries[left].Name < entries[right].Name
	})
}

// mergeEntries is array_merge() + array_values() for string keyed lists: an
// existing name keeps its position but takes the later value.
func mergeEntries(groups ...[]ListEntry) []ListEntry {
	positions := make(map[string]int, 8)
	var merged []ListEntry

	for _, group := range groups {
		for _, entry := range group {
			if at, ok := positions[entry.Name]; ok {
				merged[at] = entry
				continue
			}
			positions[entry.Name] = len(merged)
			merged = append(merged, entry)
		}
	}

	return merged
}

// wildcardDomainsLine is getWildcardDomainsLine(): the space separated list of
// wildcard domain names, cached when a Cache is configured.
func (p *Provider) wildcardDomainsLine() string {
	if nil != p.cache {
		if value, ok := p.cache.Get(WildcardCacheKey); ok {
			return value
		}
	}

	var names []string

	// The three glob() calls of the PHP method; the result is sorted below, so
	// one directory pass is equivalent.
	if entries, err := os.ReadDir(p.dir); nil == err {
		for _, entry := range entries {
			file := entry.Name()
			extension := filepath.Ext(file)
			if ".json" != extension && ".ini" != extension && ".alias" != extension {
				continue
			}
			base := strings.TrimSuffix(file, extension)
			if "default" == base || strings.Contains(base, "_wildcard_") {
				names = append(names, decodeFileName(base))
			}
		}
	}

	result := ""
	if 0 < len(names) {
		// rsort($aNames, SORT_STRING) and then array_unique().
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		result = strings.Join(unique(names), " ")
	}

	if nil != p.cache {
		_ = p.cache.Set(WildcardCacheKey, result)
	}

	return result
}

// forgetWildcardLine is `$this->oCacher && $this->oCacher->Delete(CACHE_KEY)`.
func (p *Provider) forgetWildcardLine() {
	if nil != p.cache {
		_ = p.cache.Delete(WildcardCacheKey)
	}
}

// readDisabled is getDisabled(): the entries of the "disabled" file, split on
// commas and newlines like PHP's strtok() loop and stored as lowercase punycode.
func (p *Provider) readDisabled() []string {
	content := readFileOrEmpty(filepath.Join(p.dir, "disabled"))
	if "" == content {
		return nil
	}

	items := strtok(content, ",\n")
	for index, item := range items {
		items[index] = toASCII(item)
	}

	return items
}

// saveFile is RainLoop\Utils::saveFile(): create the folder with 0700, write the
// file and restrict it to 0600.
//
// Step 5 (internal/storage) takes over this helper; it lives here so this
// package is usable on its own.
func (p *Provider) saveFile(name string, content []byte) error {
	file := filepath.Join(p.dir, name)

	if err := os.MkdirAll(filepath.Dir(file), 0o700); nil != err {
		return fmt.Errorf("create directory %s: %w", filepath.Dir(file), err)
	}
	if err := os.WriteFile(file, content, 0o600); nil != err {
		return fmt.Errorf("save file %s: %w", file, err)
	}
	// WriteFile() only applies the mode when it creates the file.
	if err := os.Chmod(file, 0o600); nil != err {
		return fmt.Errorf("chmod file %s: %w", file, err)
	}

	return nil
}

// encodeFileName is DefaultDomain::encodeFileName(): "*" is stored as "default",
// every other "*" as "_wildcard_", and the name is lowercase punycode.
func encodeFileName(name string) string {
	if "*" == name {
		return "default"
	}
	return strings.ReplaceAll(toASCII(name), "*", "_wildcard_")
}

// decodeFileName is DefaultDomain::decodeFileName().
func decodeFileName(name string) string {
	if "default" == name {
		return "*"
	}
	return strings.ReplaceAll(name, "_wildcard_", "*")
}

// stripDomainExtension is the '/\.(ini|json|alias)$/' replacement of GetList().
func stripDomainExtension(file string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(file, ".json"), ".ini"), ".alias")
}

// isFile mirrors PHP's is_file().
func isFile(name string) bool {
	info, err := os.Stat(name)
	return nil == err && info.Mode().IsRegular()
}

// readFileOrEmpty returns the content of name, or "" when it cannot be read.
func readFileOrEmpty(name string) string {
	content, err := os.ReadFile(name)
	if nil != err {
		return ""
	}
	return string(content)
}

// decodeJSONObject is json_decode($file, true) ?: array().
func decodeJSONObject(name string) map[string]any {
	content, err := os.ReadFile(name)
	if nil != err {
		return nil
	}

	var value map[string]any
	if err := json.Unmarshal(content, &value); nil != err {
		return nil
	}

	return value
}

// parseIniFile is parse_ini_file(): the flat key/value view of a domain INI
// file, without sections.
func parseIniFile(name string) map[string]string {
	content := readFileOrEmpty(name)
	if "" == content {
		return nil
	}

	result := make(map[string]string, 16)

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if "" == line || ';' == line[0] || '#' == line[0] || '[' == line[0] {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if 2 <= len(value) && '"' == value[0] && '"' == value[len(value)-1] {
			value = value[1 : len(value)-1]
		} else if separator := strings.IndexByte(value, ';'); 0 <= separator {
			value = strings.TrimSpace(value[:separator])
		}

		result[key] = value
	}

	return result
}

// contains reports whether list holds value (PHP's in_array() for strings).
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// wildcardStarRun is the '/\*+/' collapse of ValidateWildcardValues().
var wildcardStarRun = regexp.MustCompile(`\*+`)

// wildcardSeparator is the '/[\s,;]+/' split of ValidateWildcardValues().
var wildcardSeparator = regexp.MustCompile(`[\s\v,;]+`)

// ValidateWildcardValues ports RainLoop\Plugins\Helper::ValidateWildcardValues():
// it reports whether value matches one of the wildcard values and returns the
// matching item ("*" for everything).
//
// It is part of the domain provider because the wildcard domain lookup is the
// only caller in the ported feature set; step 28 (internal/plugin) exposes the
// same helper for plugin configs.
func ValidateWildcardValues(value, wildcardValues string) (bool, string) {
	value = strings.TrimSpace(value)
	if "" == value {
		return false, ""
	}

	wildcardValues = strings.TrimSpace(wildcardValues)
	if "" == wildcardValues {
		return true, ""
	}

	if "*" == wildcardValues {
		return true, "*"
	}

	for _, item := range wildcardSeparator.Split(wildcardStarRun.ReplaceAllString(wildcardValues, "*"), -1) {
		if !strings.Contains(item, "*") {
			if value == item {
				return true, item
			}
			continue
		}

		parts := strings.Split(item, "*")
		for index := range parts {
			parts[index] = regexp.QuoteMeta(parts[index])
		}
		if pattern, err := regexp.Compile(strings.Join(parts, ".*")); nil == err && pattern.MatchString(value) {
			return true, item
		}
	}

	return false, ""
}
