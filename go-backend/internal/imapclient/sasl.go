package imapclient

// The SASL mechanisms of MailSo\Imap\ImapClient::Login().
//
// They are ported in-repo rather than delegated to github.com/emersion/go-sasl +
// github.com/xdg-go/scram because the exact bytes on the wire are part of the
// compatibility contract of Step 14 (the server sees the same client-first /
// client-final messages PHP builds), and because the whole set is small enough
// to keep next to the command framing it belongs to. The mechanism preference
// order (SCRAM first) is decided by the domain config, not here.
//
// Supported: PLAIN, LOGIN, CRAM-MD5 and SCRAM-SHA-1/256/384/512/SHA3-256/384/512
// (the defaults of MailSo\Net\ConnectSettings). OAUTHBEARER/XOAUTH2 are reported
// as unsupported because the Go backend does not implement OAuth2 (see SPEC.md).

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// saslSupported is SnappyMail\SASL::isSupported(): whether the mechanism name can
// be handled by this client.
func saslSupported(mechanism string) bool {
	switch mechanism {
	case "PLAIN", "LOGIN", "CRAM-MD5":
		return true
	}
	if strings.HasPrefix(mechanism, "SCRAM-") {
		return scramHash(strings.TrimPrefix(mechanism, "SCRAM-")) != nil
	}
	return false
}

// scramHash maps a SCRAM algorithm suffix ("SHA-256", "SHA3-512", ...) onto a
// hash constructor, mirroring SnappyMail\SASL\Scram::isSupported().
func scramHash(param string) func() hash.Hash {
	switch strings.ReplaceAll(strings.ToLower(param), "sha-", "sha") {
	case "sha1":
		return sha1.New
	case "sha256":
		return sha256.New
	case "sha384":
		return sha512.New384
	case "sha512":
		return sha512.New
	case "sha3-256":
		return func() hash.Hash { return sha3.New256() }
	case "sha3-384":
		return func() hash.Hash { return sha3.New384() }
	case "sha3-512":
		return func() hash.Hash { return sha3.New512() }
	}
	return nil
}

// b64encode is base64_encode() (standard alphabet, padding).
func b64encode(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// b64decode is a lenient base64_decode(): a missing tail padding is accepted,
// exactly like PHP, which does not reject an unpadded value.
func b64decode(value string) []byte {
	value = strings.TrimSpace(value)
	if data, err := base64.StdEncoding.DecodeString(value); nil == err {
		return data
	}
	if data, err := base64.RawStdEncoding.DecodeString(value); nil == err {
		return data
	}
	return nil
}

// saslPlain builds the PLAIN initial response "\x00user\x00pass", the
// SnappyMail\SASL\Plain::authenticate() value with an empty authzid.
func saslPlain(username, password string) []byte {
	return []byte("\x00" + username + "\x00" + password)
}

// saslCramMD5 builds the CRAM-MD5 response "user hex(hmac-md5(challenge, pass))",
// the SnappyMail\SASL\Cram::authenticate() value base64 encoded.
func saslCramMD5(username, password, challengeB64 string) []byte {
	challenge := b64decode(challengeB64)
	mac := hmac.New(md5.New, []byte(password))
	mac.Write(challenge)
	digest := hex.EncodeToString(mac.Sum(nil))
	return []byte(b64encode([]byte(username + " " + digest)))
}

// scramClient is SnappyMail\SASL\Scram: one SCRAM conversation.
type scramClient struct {
	newHash         func() hash.Hash
	username        string
	password        string
	nonce           string
	gs2Header       string
	clientFirstBare string
	serverSignature []byte
}

// newScram starts a SCRAM conversation for the given "SCRAM-…" mechanism. A nil
// result means the algorithm is not supported.
func newScram(mechanism, username, password string) *scramClient {
	newHash := scramHash(strings.TrimPrefix(mechanism, "SCRAM-"))
	if nil == newHash {
		return nil
	}

	// SASLprep of '=' and ',' per RFC 5802 §5.1.
	username = strings.NewReplacer("=", "=3D", ",", "=2C").Replace(username)

	nonce := randomHex(16)

	const gs2Header = "n,,"
	return &scramClient{
		newHash:         newHash,
		username:        username,
		password:        password,
		nonce:           nonce,
		gs2Header:       gs2Header,
		clientFirstBare: "n=" + username + ",r=" + nonce,
	}
}

// clientFirst is the client-first-message sent inside the AUTHENTICATE command.
func (s *scramClient) clientFirst() []byte {
	return []byte(s.gs2Header + s.clientFirstBare)
}

// clientFinal consumes the server-first message (base64 on the wire) and returns
// the client-final message, already base64 encoded and ready to send.
func (s *scramClient) clientFinal(serverFirstB64 string) (string, error) {
	serverFirst := b64decode(serverFirstB64)
	values := parseScramMessage(string(serverFirst))

	serverNonce := values["r"]
	if "" == serverNonce {
		return "", errors.New("imapclient: scram server nonce not found")
	}
	if !strings.HasPrefix(serverNonce, s.nonce) {
		return "", errors.New("imapclient: scram server invalid nonce")
	}

	salt := b64decode(values["s"])
	if 0 == len(salt) {
		return "", errors.New("imapclient: scram server invalid salt")
	}

	iterations, convErr := strconv.Atoi(values["i"])
	if nil != convErr || 0 >= iterations {
		return "", errors.New("imapclient: scram server invalid iteration count")
	}

	saltedPassword := pbkdf2.Key([]byte(s.password), salt, iterations, s.newHash().Size(), s.newHash)

	clientKey := scramHMAC(s.newHash, saltedPassword, []byte("Client Key"))
	storedKey := scramHashSum(s.newHash, clientKey)

	clientFinalWithoutProof := "c=" + b64encode([]byte(s.gs2Header)) + ",r=" + serverNonce
	authMessage := s.clientFirstBare + "," + string(serverFirst) + "," + clientFinalWithoutProof

	clientSignature := scramHMAC(s.newHash, storedKey, []byte(authMessage))
	proof := xorBytes(clientKey, clientSignature)

	serverKey := scramHMAC(s.newHash, saltedPassword, []byte("Server Key"))
	s.serverSignature = scramHMAC(s.newHash, serverKey, []byte(authMessage))

	clientFinal := clientFinalWithoutProof + ",p=" + b64encode(proof)
	return b64encode([]byte(clientFinal)), nil
}

// verify checks the server-final message (base64 on the wire) against the
// computed server signature.
func (s *scramClient) verify(serverFinalB64 string) error {
	values := parseScramMessage(string(b64decode(serverFinalB64)))
	signature := values["v"]
	if "" == signature {
		return errors.New("imapclient: scram server signature not found")
	}
	if b64encode(s.serverSignature) != signature {
		return errors.New("imapclient: scram server signature invalid")
	}
	return nil
}

// parseScramMessage parses a SCRAM "k=v,k=v" message, like
// SnappyMail\SASL\Scram::parseMessage().
func parseScramMessage(message string) map[string]string {
	values := map[string]string{}
	for _, field := range strings.Split(message, ",") {
		key, value, found := strings.Cut(field, "=")
		if !found || "" == key {
			continue
		}
		values[key] = value
	}
	return values
}

// scramHMAC is hash_hmac(algo, message, key, true).
func scramHMAC(newHash func() hash.Hash, key, message []byte) []byte {
	mac := hmac.New(newHash, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// scramHashSum is hash(algo, data, true).
func scramHashSum(newHash func() hash.Hash, data []byte) []byte {
	h := newHash()
	h.Write(data)
	return h.Sum(nil)
}

// xorBytes is the PHP "^" on two equal length byte strings.
func xorBytes(left, right []byte) []byte {
	length := len(left)
	if len(right) < length {
		length = len(right)
	}
	result := make([]byte, length)
	for index := 0; index < length; index++ {
		result[index] = left[index] ^ right[index]
	}
	return result
}

// randomHex is bin2hex(random_bytes(size)).
func randomHex(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); nil != err {
		return ""
	}
	return hex.EncodeToString(buffer)
}
