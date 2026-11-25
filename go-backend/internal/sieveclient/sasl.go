package sieveclient

// The SASL part of MailSo\Sieve\SieveClient::Login().
//
// Only PLAIN and LOGIN are needed by RFC 5804 servers, and both are three lines
// of base64, so they are implemented here instead of being delegated to
// github.com/emersion/go-sasl: the exact bytes on the wire are what the
// compatibility contract is about (SnappyMail\SASL\Plain::authenticate() sends
// "\x00user\x00pass" base64 encoded, SnappyMail\SASL\Login::authenticate() sends
// the base64 user name and challenge('') the base64 password).

import "encoding/base64"

// b64encode is base64_encode() (standard alphabet, padding).
func b64encode(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// saslPlain builds the PLAIN initial response "\x00user\x00pass", the
// SnappyMail\SASL\Plain::authenticate() value with an empty authzid.
func saslPlain(username, password string) []byte {
	return []byte("\x00" + username + "\x00" + password)
}
