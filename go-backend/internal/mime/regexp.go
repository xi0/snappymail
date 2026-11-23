package mime

import "regexp"

// mustRegexp compiles pattern, panicking on a malformed expression (the
// patterns are constants, so a panic is a programming error).
func mustRegexp(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
