package web

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

// secFetchRule matches "dest|mode|site=<value>" pairs, like PHP's
// preg_match_all('/(dest|mode|site)=([^,]+)/', $rule).
var secFetchRule = regexp.MustCompile(`(dest|mode|site)=([^,]+)`)

// SecFetchMatchAnyRule mirrors \SnappyMail\HTTP\SecFetch::matchAnyRule().
//
// Requests without a Sec-Fetch-Site header (every browser that does not
// implement Fetch Metadata, and every non-browser client) always pass. The
// default rules "site=same-origin;site=none" are always allowed; rules is
// security.secfetch_allow.
func SecFetchMatchAnyRule(r *http.Request, rules string) bool {
	if !httpx.HasHeader(r, "Sec-Fetch-Site") {
		return true
	}

	for _, rule := range strings.Split("site=same-origin;site=none;"+rules, ";") {
		matches := secFetchRule.FindAllStringSubmatch(rule, -1)
		if 0 == len(matches) {
			continue
		}

		values := map[string]string{"dest": "", "mode": "", "site": ""}
		for _, match := range matches {
			values[match[1]] = match[2]
		}

		if !siteMatches(r, values["site"]) {
			continue
		}
		if "" != values["dest"] && httpx.HasHeader(r, "Sec-Fetch-Dest") &&
			values["dest"] != httpx.GetHeader(r, "Sec-Fetch-Dest") {
			continue
		}
		if "" != values["mode"] && httpx.HasHeader(r, "Sec-Fetch-Mode") &&
			values["mode"] != httpx.GetHeader(r, "Sec-Fetch-Mode") {
			continue
		}

		return true
	}

	return false
}

// siteMatches reports whether the site part of a rule allows the request. An
// empty site in the rule matches every request.
func siteMatches(r *http.Request, expected string) bool {
	if "" == expected {
		return true
	}
	return expected == httpx.GetHeader(r, "Sec-Fetch-Site")
}

// secFetchUser mirrors \SnappyMail\HTTP\SecFetch::user().
func secFetchUser(r *http.Request) bool {
	return "?1" == httpx.GetHeader(r, "Sec-Fetch-User")
}

// secFetchReport builds the description the Sec-Fetch gate puts into the error
// template ("Disallowed Sec-Fetch" + the three request headers + user flag).
func secFetchReport(r *http.Request) string {
	user := "false"
	if secFetchUser(r) {
		user = "true"
	}

	return "Disallowed Sec-Fetch\n" +
		"Dest: " + httpx.GetHeader(r, "Sec-Fetch-Dest") + "\n" +
		"Mode: " + httpx.GetHeader(r, "Sec-Fetch-Mode") + "\n" +
		"Site: " + httpx.GetHeader(r, "Sec-Fetch-Site") + "\n" +
		"User: " + user
}
