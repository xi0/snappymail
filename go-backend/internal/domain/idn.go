package domain

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Limits of RFC 5890 / UTS46 (SnappyMail\Intl\Idn::MAX_DOMAIN_SIZE /
// MAX_LABEL_SIZE).
const (
	// maxDomainSize is the maximum length of an ASCII domain name, excluding
	// the dot of the root label.
	maxDomainSize = 253
	// maxLabelSize is the maximum length of one ASCII label.
	maxLabelSize = 63
)

// nameProfile is the UTS #46 ToASCII profile PHP's idn_to_ascii() uses when
// SnappyMail stores a domain name. PHP calls it with the default flags
// (IDNA_DEFAULT, variant INTL_IDNA_VARIANT_UTS46), which means:
//
//   - transitional processing, so the deviation characters map (ß becomes ss),
//     because IDNA_NONTRANSITIONAL_TO_ASCII is not set;
//   - no STD3 rules (IDNA_USE_STD3_RULES is not set), so "*" in a wildcard file
//     name and "_" survive instead of being rejected;
//   - no IDNA_CHECK_BIDI and no IDNA_CHECK_CONTEXTJ.
//
// VerifyDNSLength stays off: ICU (and SnappyMail's polyfill) allow the empty
// root label of an absolute name ("example.com."), which x/net/idna's length
// check rejects along with the length limits, so both are applied by
// validLabels() below.
var nameProfile = idna.New(
	idna.MapForLookup(),
	idna.Transitional(true),
	idna.StrictDomainName(false),
	idna.VerifyDNSLength(false),
)

// toASCII ports \idn_to_ascii() the way SnappyMail calls it (UTS46 ToASCII with
// transitional processing, PHP's default) and returns the name in lowercase
// punycode.
//
// An invalid name returns "" like PHP's false, which makes the callers fall back
// to "not found" and "cannot save".
func toASCII(name string) string {
	if "" == name || !utf8.ValidString(name) {
		return ""
	}

	ascii, err := nameProfile.ToASCII(name)
	if nil != err {
		// The label checks of UTS #46 section 4.1 that x/net/idna performs:
		// hyphens in the third and fourth position, a leading or trailing
		// hyphen, a leading combining mark and disallowed code points.
		return ""
	}

	if !validLabels(ascii) {
		return ""
	}

	return ascii
}

// validLabels applies the two UTS #46 section 4.1 validity criteria that are
// missing from nameProfile: there must be no empty label (except for the root
// label of an absolute name) and ToASCII verifies the DNS length.
//
// The limits are checked against the ASCII (punycode) form, like
// SnappyMail\Intl\Idn::validateDomainAndLabelLength() and ICU do. The label
// checks themselves (hyphens, combining marks, disallowed code points and the
// decoding of an "xn--" label) are done by nameProfile.
func validLabels(ascii string) bool {
	labels := strings.Split(ascii, ".")
	count := len(labels)

	// Number of "." separators.
	domainLength := count - 1
	limit := maxDomainSize

	// A single trailing dot is the root label of an absolute name: it adds the
	// byte UTS46 allows and is not subject to the label checks.
	if 1 < count && "" == labels[count-1] {
		limit++
		count--
	}

	for _, label := range labels[:count] {
		if "" == label {
			return false
		}
		domainLength += len(label)
		if maxLabelSize < len(label) {
			return false
		}
	}

	return limit >= domainLength
}

// EmailToASCII is SnappyMail\IDN::emailToAscii(): only the domain part after the
// last "@" is converted, and an address without "@" is returned unchanged.
func EmailToASCII(address string) string {
	index := strings.LastIndex(address, "@")
	if 0 > index {
		return address
	}
	return address[:index+1] + toASCII(address[index+1:])
}
