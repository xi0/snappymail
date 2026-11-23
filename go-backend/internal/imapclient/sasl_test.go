package imapclient

// Tests of the in-repo SASL implementation and the connection-security helpers.

import (
	"crypto/sha1"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// TestScramSHA1Vector checks the SCRAM-SHA-1 exchange against the RFC 5802 §5
// example ("user"/"pencil"). The nonce is fixed to the RFC value.
func TestScramSHA1Vector(t *testing.T) {
	scram := &scramClient{
		newHash:         sha1.New,
		password:        "pencil",
		nonce:           "fyko+d2lbbFgONRv9qkxdawL",
		gs2Header:       "n,,",
		clientFirstBare: "n=user,r=fyko+d2lbbFgONRv9qkxdawL",
	}

	serverFirst := "r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j,s=QSXCR+Q6sek8bf92,i=4096"

	got, err := scram.clientFinal(b64encode([]byte(serverFirst)))
	if nil != err {
		t.Fatalf("clientFinal: %v", err)
	}

	const wantClientFinal = "c=biws,r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j," +
		"p=v0X8v3Bz2T0CJGbJQyF0X+HI4Ts="
	if want := b64encode([]byte(wantClientFinal)); got != want {
		t.Errorf("client-final mismatch\n got: %s\nwant: %s", got, want)
	}

	const serverFinal = "v=rmF9pqV8S7suAoZWja4dJRkFsKQ="
	if err := scram.verify(b64encode([]byte(serverFinal))); nil != err {
		t.Errorf("verify: %v", err)
	}
}

func TestScramClientFirst(t *testing.T) {
	scram := newScram("SCRAM-SHA-256", "user", "pencil")
	if nil == scram {
		t.Fatal("SCRAM-SHA-256 should be supported")
	}
	first := string(scram.clientFirst())
	if want := "n,,n=user,r="; len(first) <= len(want) || first[:len(want)] != want {
		t.Errorf("client-first = %q", first)
	}

	// Unsupported SCRAM algorithms and channel binding are rejected.
	if nil != newScram("SCRAM-SHA-9999", "user", "pencil") {
		t.Error("an unknown SCRAM algorithm must not be supported")
	}
}

func TestScramBadNonce(t *testing.T) {
	scram := newScram("SCRAM-SHA-256", "user", "pencil")
	// A server nonce that does not extend the client nonce must be rejected.
	_, err := scram.clientFinal(b64encode([]byte("r=attacker,s=QSXCR+Q6sek8bf92,i=4096")))
	if nil == err {
		t.Fatal("a mismatching server nonce must fail")
	}
}

func TestSASLSupported(t *testing.T) {
	for _, mechanism := range []string{
		"PLAIN", "LOGIN", "CRAM-MD5",
		"SCRAM-SHA-1", "SCRAM-SHA-256", "SCRAM-SHA-512", "SCRAM-SHA3-512",
	} {
		if !saslSupported(mechanism) {
			t.Errorf("%s should be supported", mechanism)
		}
	}
	for _, mechanism := range []string{"XOAUTH2", "OAUTHBEARER", "SCRAM-SHA-9999", ""} {
		if saslSupported(mechanism) {
			t.Errorf("%s should not be supported", mechanism)
		}
	}
}

func TestSASLPlain(t *testing.T) {
	if got := string(saslPlain("user", "pass")); "\x00user\x00pass" != got {
		t.Errorf("saslPlain = %q", got)
	}
}

func TestUseSSL(t *testing.T) {
	cases := []struct {
		port int
		kind int
		want bool
	}{
		{993, domain.SecurityAutoDetect, true},
		{995, domain.SecurityAutoDetect, true},
		{465, domain.SecurityAutoDetect, true},
		{143, domain.SecurityAutoDetect, false},
		{4190, domain.SecurityAutoDetect, false},
		{143, domain.SecuritySSL, true},
		{993, domain.SecurityNone, false},
		{143, domain.SecuritySTARTTLS, false},
	}
	for _, test := range cases {
		if got := useSSL(test.port, test.kind); got != test.want {
			t.Errorf("useSSL(%d, %d) = %v, want %v", test.port, test.kind, got, test.want)
		}
	}
}

func TestQuoteString(t *testing.T) {
	if got := quoteString(`a"b\c`); `"a\"b\\c"` != got {
		t.Errorf("quoteString = %s", got)
	}
	if got := quoteString("plain"); `"plain"` != got {
		t.Errorf("quoteString = %s", got)
	}
}

func TestLatin1(t *testing.T) {
	// Characters inside Latin-1 pass through unchanged.
	if got := latin1("pässwörd"); "pässwörd" != got {
		t.Errorf("latin1 = %q", got)
	}
	// A character outside Latin-1 is replaced with '?'.
	if got := latin1("p😀"); "p?" != got {
		t.Errorf("latin1 = %q", got)
	}
}
