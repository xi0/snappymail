package imapclient

import "regexp"

// mustRegexp compiles a constant pattern, panicking on a programming error.
func mustRegexp(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// regexpQuote is preg_quote() for the ASCII names used in patterns.
func regexpQuote(value string) string {
	return regexp.QuoteMeta(value)
}
