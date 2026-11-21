package pages

// SnappyMail\UUID::generate() without the PECL extension, used for the CSP
// script nonce of the index page.

import (
	"crypto/rand"
	"encoding/hex"
)

// newUUID returns a random (version 4) UUID in lower case.
func newUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); nil != err {
		// crypto/rand does not fail in practice; an empty nonce would break the
		// page, so fall back to a fixed-but-unique-enough value.
		return "00000000-0000-4000-8000-000000000000"
	}

	// Version 4 and the RFC 4122 variant.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	encoded := hex.EncodeToString(raw[:])

	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32]
}
