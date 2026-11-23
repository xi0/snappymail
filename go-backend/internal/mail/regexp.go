package mail

import (
	"encoding/base64"
	"regexp"
)

// mustRegexp compiles a constant pattern, panicking on a programming error.
func mustRegexp(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// base64StdDecode is PHP's base64_decode() in strict mode.
func base64StdDecode(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if nil != err {
		return "", err
	}
	return string(decoded), nil
}
