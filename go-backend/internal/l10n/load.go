package l10n

// Port of SnappyMail\L10n::load(): the en -> base -> exact language merge.
//
// The on-disk files are JSON objects, so the merged document is a
// map[string]any with the same nesting the frontend expects. The merge order
// is the contract: an "sv-SE" request loads en, then sv, then sv-SE, each
// overriding the previous one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Load is L10n::load(): the merge of en, the base language and the requested
// language for the given file ("user", "admin" or "static").
//
// A missing file is skipped, exactly like the is_file() guard of PHP; an
// unreadable or invalid JSON file is skipped as well so a broken localization
// cannot take the request down.
func (c *Catalog) Load(language, file string) map[string]any {
	result := map[string]any{}
	if nil == c {
		return result
	}

	language = strings.ReplaceAll(language, "_", "-")

	// array_unique(['en', explode('-', $sLanguage)[0], $sLanguage]) keeps the
	// first occurrence order.
	candidates := []string{"en"}
	if base, _, found := strings.Cut(language, "-"); found {
		candidates = append(candidates, base)
	}
	candidates = append(candidates, language)

	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true

		document, ok := c.readJSON(candidate, file)
		if !ok {
			continue
		}
		result = MergeRecursive(result, document)
	}

	return result
}

// readJSON reads <localizationDir>/<language>/<file>.json.
func (c *Catalog) readJSON(language, file string) (map[string]any, bool) {
	if "" == language || strings.ContainsAny(language, `/\`) {
		return nil, false
	}

	path := filepath.Join(c.localizationDir, language, file+".json")

	content, err := os.ReadFile(path)
	if nil != err {
		return nil, false
	}

	var document map[string]any
	if err := json.Unmarshal(content, &document); nil != err || nil == document {
		return nil, false
	}

	return document, true
}

// MergeRecursive is PHP's array_replace_recursive(): every key of src replaces
// (or, for two nested arrays, merges into) the key of dst.
//
// dst is modified in place and returned for convenience.
func MergeRecursive(dst, src map[string]any) map[string]any {
	if nil == dst {
		dst = map[string]any{}
	}

	for key, value := range src {
		if existing, ok := dst[key]; ok {
			dst[key] = mergeValue(existing, value)
			continue
		}
		dst[key] = value
	}

	return dst
}

// mergeValue is the recursive part of array_replace_recursive() for one key.
func mergeValue(existing, value any) any {
	if left, ok := existing.(map[string]any); ok {
		if right, ok := value.(map[string]any); ok {
			return MergeRecursive(left, right)
		}
	}

	if left, ok := existing.([]any); ok {
		if right, ok := value.([]any); ok {
			return mergeSlices(left, right)
		}
	}

	return value
}

// mergeSlices is array_replace_recursive() for two JSON arrays: the numeric
// keys of src replace the ones of dst, appending the extra entries.
func mergeSlices(dst, src []any) []any {
	for index, value := range src {
		if index < len(dst) {
			dst[index] = mergeValue(dst[index], value)
			continue
		}
		dst = append(dst, value)
	}
	return dst
}

// IntersectKeys is array_intersect_key(): the entries of document whose key is
// in keep, keeping the entry order of document.
func IntersectKeys(document map[string]any, keep map[string]bool) map[string]any {
	result := make(map[string]any, len(document))
	for key, value := range document {
		if keep[key] {
			result[key] = value
		}
	}
	return result
}

// sortedKeys returns the keys of document sorted ascending (only used by the
// tests and by callers that need deterministic iteration).
func sortedKeys(document map[string]any) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
