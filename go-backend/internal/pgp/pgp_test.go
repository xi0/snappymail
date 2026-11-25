package pgp

// Tests of the Step 25 OpenPGP engine and of the parsers it shares with the
// gpg binary shim: the sign/verify and encrypt/decrypt round trips, the
// keyInfo() shape, the UID/colon/status parsers and the keyserver index parser.

import (
	"strings"
	"testing"
)

// testPassphrase is the passphrase of the generated test key.
const testPassphrase = "s3cret"

// newTestEngine builds the Go engine in a temporary home directory.
func newTestEngine(t *testing.T) Engine {
	t.Helper()

	engine, err := NewOpenPGP(t.TempDir())
	if nil != err {
		t.Fatalf("NewOpenPGP: %v", err)
	}
	return engine
}

// generateTestKey creates a key pair and returns its fingerprint.
func generateTestKey(t *testing.T, engine Engine) string {
	t.Helper()

	fingerprint, err := engine.GenerateKey("Test User <test@example.com>", testPassphrase)
	if nil != err {
		t.Fatalf("GenerateKey: %v", err)
	}
	if "" == fingerprint {
		t.Fatal("GenerateKey returned no fingerprint")
	}
	return fingerprint
}

// TestGenerateKeyInfo checks the keyInfo()/allKeysInfo() documents of a freshly
// generated key: the uid split, the subkey record and the renamed capabilities.
func TestGenerateKeyInfo(t *testing.T) {
	engine := newTestEngine(t)
	fingerprint := generateTestKey(t, engine)

	keys := engine.KeyInfo("", false)
	if 1 != len(keys) {
		t.Fatalf("KeyInfo returned %d keys, want 1", len(keys))
	}

	key := keys[0]
	// PHP quirk: keyInfo() sets is_secret to ("ssb" === $tokens[0]), which is
	// false for the 'sec'/'pub' record of the primary key.
	if key.IsSecret {
		t.Error("keyInfo() reports is_secret false for the primary key")
	}
	if !key.CanSign || !key.CanEncrypt {
		t.Errorf("capabilities = sign:%v encrypt:%v, want both", key.CanSign, key.CanEncrypt)
	}
	if 0 == len(key.UIDs) {
		t.Fatal("the key has no uid")
	}
	if "Test User" != key.UIDs[0].Name || "test@example.com" != key.UIDs[0].Email {
		t.Errorf("uid = %+v", key.UIDs[0])
	}
	if 0 == len(key.Subkeys) {
		t.Fatal("the key has no subkey record")
	}
	if key.Subkeys[0].Fingerprint != fingerprint {
		t.Errorf("primary fingerprint = %q, want %q", key.Subkeys[0].Fingerprint, fingerprint)
	}
	if 40 != len(key.Subkeys[0].Fingerprint) {
		t.Errorf("fingerprint length = %d", len(key.Subkeys[0].Fingerprint))
	}
	if key.Subkeys[0].KeyID != key.Subkeys[0].Fingerprint[24:] {
		t.Errorf("key id %q is not the tail of the fingerprint", key.Subkeys[0].KeyID)
	}
	if "1" != key.Subkeys[0].Algorithm {
		t.Errorf("algorithm = %q, want 1 (RSA)", key.Subkeys[0].Algorithm)
	}
	if "" == key.Subkeys[0].Length || "0" == key.Subkeys[0].Length {
		t.Errorf("length = %q", key.Subkeys[0].Length)
	}

	// The public listing renames can_sign into can_verify, the private listing
	// can_encrypt into can_decrypt (allKeysInfo()).
	all := engine.AllKeysInfo("")
	if 1 != len(all.Public) || 1 != len(all.Private) {
		t.Fatalf("allKeysInfo sizes = %d/%d", len(all.Public), len(all.Private))
	}
	if !all.Public[0].CanVerify || all.Public[0].CanSign {
		t.Errorf("public key = %+v", all.Public[0])
	}
	if !all.Private[0].CanDecrypt || all.Private[0].CanEncrypt {
		t.Errorf("private key = %+v", all.Private[0])
	}

	// A pattern filters on the uid.
	if 0 != len(engine.KeyInfo("other@example.com", false)) {
		t.Error("a non matching pattern must not find the key")
	}
	if 1 != len(engine.KeyInfo("test@example.com", false)) {
		t.Error("a matching pattern must find the key")
	}
}

// TestSignAndVerify checks the clearsigned round trip, including the "unknown
// key" answer of a verification without the public key.
func TestSignAndVerify(t *testing.T) {
	engine := newTestEngine(t)
	fingerprint := generateTestKey(t, engine)

	signed, err := engine.Sign("Hello OpenPGP\r\n")
	if nil != err {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.HasPrefix(signed, "-----BEGIN PGP SIGNED MESSAGE-----") {
		t.Fatalf("Sign output = %q", signed)
	}

	signatures, err := engine.Verify(signed, "")
	if nil != err {
		t.Fatalf("Verify: %v", err)
	}
	if 1 != len(signatures) {
		t.Fatalf("signatures = %+v", signatures)
	}
	if fingerprint != signatures[0].Fingerprint {
		t.Errorf("fingerprint = %q, want %q", signatures[0].Fingerprint, fingerprint)
	}
	if 0 != signatures[0].Status || 0 != signatures[0].Summary {
		t.Errorf("status/summary = %d/%d, want 0/0", signatures[0].Status, signatures[0].Summary)
	}

	// A keyring without the key reports the ERRSIG entry, like gpg does.
	other := newTestEngine(t)
	unknown, err := other.Verify(signed, "")
	if nil != err {
		t.Fatalf("Verify (unknown key): %v", err)
	}
	if 1 != len(unknown) || 1 != unknown[0].Status || 4 != unknown[0].Summary {
		t.Errorf("unknown key signature = %+v", unknown)
	}

	// Plain text without a signature yields nothing at all.
	none, err := engine.Verify("not signed", "")
	if nil != err {
		t.Fatalf("Verify (plain): %v", err)
	}
	if 0 != len(none) {
		t.Errorf("plain text verified: %+v", none)
	}
}

// TestEncryptAndDecrypt checks the armor encryption round trip with the
// remembered passphrase.
func TestEncryptAndDecrypt(t *testing.T) {
	engine := newTestEngine(t)
	fingerprint := generateTestKey(t, engine)

	if !engine.AddEncryptKey(fingerprint) {
		t.Fatal("AddEncryptKey failed")
	}

	encrypted, err := engine.Encrypt("top secret\r\n")
	if nil != err {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(encrypted, "-----BEGIN PGP MESSAGE-----") {
		t.Fatalf("Encrypt output = %q", encrypted)
	}

	// Without the passphrase the decryption fails.
	engine.ClearKeys()
	if _, err := engine.Decrypt(encrypted); nil == err {
		t.Error("Decrypt without a passphrase must fail")
	}

	if !engine.AddDecryptKey(fingerprint, testPassphrase) {
		t.Fatal("AddDecryptKey failed")
	}
	plaintext, err := engine.Decrypt(encrypted)
	if nil != err {
		t.Fatalf("Decrypt: %v", err)
	}
	if "top secret\r\n" != plaintext {
		t.Errorf("plaintext = %q", plaintext)
	}
}

// TestExportImportRoundTrip checks the armored export, the import counters and
// the deletion of a key.
func TestExportImportRoundTrip(t *testing.T) {
	engine := newTestEngine(t)
	fingerprint := generateTestKey(t, engine)

	publicKey, err := engine.Export(fingerprint, nil)
	if nil != err {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(publicKey, "PGP PUBLIC KEY BLOCK") || strings.Contains(publicKey, "PRIVATE") {
		t.Fatalf("public export = %q", publicKey)
	}

	if _, err := engine.Export("0xDEADBEEF", nil); nil == err {
		t.Error("exporting an unknown key must fail")
	}

	other := newTestEngine(t)
	stats, err := other.Import(publicKey)
	if nil != err {
		t.Fatalf("Import: %v", err)
	}
	if 1 != stats.Imported {
		t.Errorf("imported = %d, want 1", stats.Imported)
	}

	// Importing the same block again changes nothing.
	stats, err = other.Import(publicKey)
	if nil != err {
		t.Fatalf("Import (again): %v", err)
	}
	if 0 != stats.Imported || 1 != stats.Unchanged {
		t.Errorf("second import = %+v", stats)
	}

	if _, err := other.Import("no key here"); nil == err {
		t.Error("importing garbage must fail")
	}

	if _, err := other.Export(fingerprint, nil); nil != err {
		t.Errorf("the imported key must be exportable: %v", err)
	}

	if deleted, err := other.DeleteKey(fingerprint, false); nil != err || !deleted {
		t.Fatalf("DeleteKey = %v, %v", deleted, err)
	}
	if 0 != len(other.KeyInfo("", false)) {
		t.Error("the key was not removed")
	}
	if _, err := other.DeleteKey(fingerprint, false); nil == err {
		t.Error("deleting a missing key must fail")
	}
}

// TestExportPrivateKey checks the key pair export with the passphrase.
func TestExportPrivateKey(t *testing.T) {
	engine := newTestEngine(t)
	fingerprint := generateTestKey(t, engine)

	wrong := "wrong"
	if _, err := engine.Export(fingerprint, &wrong); nil == err {
		t.Error("a wrong passphrase must fail")
	}

	passphrase := testPassphrase
	privateKey, err := engine.Export(fingerprint, &passphrase)
	if nil != err {
		t.Fatalf("Export (private): %v", err)
	}
	if !strings.Contains(privateKey, "PGP PRIVATE KEY BLOCK") {
		t.Fatalf("private export = %q", privateKey)
	}

	// The exported pair can be imported into a second engine and used.
	other := newTestEngine(t)
	if _, err := other.Import(privateKey); nil != err {
		t.Fatalf("Import (private): %v", err)
	}
	if 0 == len(other.KeyInfo("", true)) {
		t.Error("the imported key pair is not listed")
	}
}

// TestParseUID checks the uid record parser (Name (Comment) <email>).
func TestParseUID(t *testing.T) {
	tests := []struct {
		raw     string
		name    string
		comment string
		email   string
	}{
		{"John Doe <john@example.com>", "John Doe", "", "john@example.com"},
		{"John Doe (work) <john@example.com>", "John Doe", "work", "john@example.com"},
		{"john@example.com", "", "", "john@example.com"},
		{"John Doe", "John Doe", "", ""},
		{"(only comment)", "(only comment)", "", ""},
		{`Jos\xc3\xa9 <jose@example.com>`, "José", "", "jose@example.com"},
	}

	for _, test := range tests {
		uid := parseUID(test.raw)
		if uid.Name != test.name || uid.Comment != test.comment || uid.Email != test.email {
			t.Errorf("parseUID(%q) = %+v", test.raw, uid)
		}
		if uid.UID != test.raw {
			t.Errorf("parseUID(%q).UID = %q", test.raw, uid.UID)
		}
	}
}

// TestParseColons checks the `gpg --with-colons` state machine against a fixture
// that has a primary key, a subkey, a fingerprint and a uid.
func TestParseColons(t *testing.T) {
	output := strings.Join([]string{
		"tru::1:1600000000:0:3:1:5",
		"sec:u:2048:1:0123456789ABCDEF:1600000000:::u:::esca::::::23::0:",
		"fpr:::::::::1111111111111111111111111111111111111111:",
		"grp:::::::::GRIPGRIPGRIPGRIPGRIPGRIPGRIPGRIPGRIPGRIP:",
		"uid:u::::1600000000::HASH::John Doe (work) <john@example.com>::::::::::0:",
		"ssb:u:2048:1:FEDCBA9876543210:1600000000:::u:::e::::::23::0:",
		"fpr:::::::::2222222222222222222222222222222222222222:",
		"",
	}, "\n")

	keys := parseColons(output)
	if 1 != len(keys) {
		t.Fatalf("keys = %d", len(keys))
	}

	key := keys[0]
	if !key.CanSign || !key.CanEncrypt {
		t.Errorf("capabilities = %+v", key)
	}
	if 1 != len(key.UIDs) {
		t.Fatalf("uids = %+v", key.UIDs)
	}
	if "John Doe" != key.UIDs[0].Name || "work" != key.UIDs[0].Comment || "john@example.com" != key.UIDs[0].Email {
		t.Errorf("uid = %+v", key.UIDs[0])
	}
	if 2 != len(key.Subkeys) {
		t.Fatalf("subkeys = %+v", key.Subkeys)
	}
	if "1111111111111111111111111111111111111111" != key.Subkeys[0].Fingerprint {
		t.Errorf("primary fingerprint = %q", key.Subkeys[0].Fingerprint)
	}
	if "GRIPGRIPGRIPGRIPGRIPGRIPGRIPGRIPGRIPGRIP" != key.Subkeys[0].Keygrip {
		t.Errorf("keygrip = %q", key.Subkeys[0].Keygrip)
	}
	if !key.Subkeys[1].CanEncrypt || key.Subkeys[1].CanSign {
		t.Errorf("subkey capabilities = %+v", key.Subkeys[1])
	}
	if "FEDCBA9876543210" != key.Subkeys[1].KeyID {
		t.Errorf("subkey keyid = %q", key.Subkeys[1].KeyID)
	}
}

// TestParseVerifyStatus checks the GOODSIG/VALIDSIG/BADSIG mapping.
func TestParseVerifyStatus(t *testing.T) {
	good := parseVerifyStatus([]string{
		"GOODSIG 0123456789ABCDEF John Doe <john@example.com>",
		"VALIDSIG 1111111111111111111111111111111111111111 2021-01-01 1600000000 0 4 1 22 8 00 2222",
		"TRUST_FULLY 0 PGPV",
	})
	if 1 != len(good) {
		t.Fatalf("good = %+v", good)
	}
	if 0 != good[0].Status || 0 != good[0].Summary {
		t.Errorf("good status/summary = %d/%d", good[0].Status, good[0].Summary)
	}
	if "1111111111111111111111111111111111111111" != good[0].Fingerprint {
		t.Errorf("good fingerprint = %q", good[0].Fingerprint)
	}
	if 1600000000 != good[0].Timestamp || 4 != good[0].Version {
		t.Errorf("good timestamp/version = %d/%d", good[0].Timestamp, good[0].Version)
	}

	bad := parseVerifyStatus([]string{"BADSIG 0123456789ABCDEF John Doe"})
	if 1 != len(bad) || 1 != bad[0].Status || 4 != bad[0].Summary {
		t.Errorf("bad = %+v", bad)
	}

	if 0 != len(parseVerifyStatus(nil)) {
		t.Error("no status lines must yield no signatures")
	}
}

// TestParseKeyServerIndex checks the machine readable HKP answer.
func TestParseKeyServerIndex(t *testing.T) {
	body := strings.Join([]string{
		"pub:0123456789ABCDEF:1:2048:1600000000::",
		"uid:John%20Doe%20%3Cjohn%40example.com%3E:1600000000::",
		"pub:FEDCBA9876543210:1:2048:1600000000:1000000000:",
		"uid:Expired%20Key:1600000000:1000000000:",
		"pub:0000000000000000:1:2048:1600000000::r",
		"",
	}, "\n")

	keys := parseKeyServerIndex(body, "https://keys.example.com")
	if 1 != len(keys) {
		t.Fatalf("keys = %+v", keys)
	}
	if "0123456789ABCDEF" != keys[0].KeyID {
		t.Errorf("keyid = %q", keys[0].KeyID)
	}
	if "https://keys.example.com" != keys[0].Host {
		t.Errorf("host = %q", keys[0].Host)
	}
	if 1 != keys[0].Algo || 2048 != keys[0].KeyLen {
		t.Errorf("algo/keylen = %d/%d", keys[0].Algo, keys[0].KeyLen)
	}
	if nil == keys[0].CreationDate || 1600000000 != *keys[0].CreationDate {
		t.Errorf("creationdate = %v", keys[0].CreationDate)
	}
	if nil != keys[0].ExpirationDate {
		t.Errorf("expirationdate = %v", keys[0].ExpirationDate)
	}
	if 1 != len(keys[0].UIDs) || "John Doe <john@example.com>" != keys[0].UIDs[0].UID {
		t.Errorf("uids = %+v", keys[0].UIDs)
	}
}

// TestPHPJSONArray checks the json_encode() layout of the stored key blob.
func TestPHPJSONArray(t *testing.T) {
	value := phpJSONArray([]string{`a/b`, "c\"d", "\x01"})
	if `["a\/b","c\"d","\u0001"]` != value {
		t.Errorf("phpJSONArray = %s", value)
	}
}

// TestHMACSHA1 checks hash_hmac('sha1', …) of the stored blob against the
// reference vector of RFC 2202 (key "key", the fox sentence).
func TestHMACSHA1(t *testing.T) {
	const message = "The quick brown fox jumps over the lazy dog"
	const expected = "de7c9b85b8b78aa6bc8a7a36f70a90701c9db4d9"

	if got := hmacSHA1(message, "key"); expected != got {
		t.Errorf("hmacSHA1 = %q, want %q", got, expected)
	}
	if hmacSHA1(message, "key") == hmacSHA1(message, "other") {
		t.Error("hmacSHA1 ignores the key")
	}
}

// TestFindBinary checks that a missing binary is reported as "" instead of
// panicking, so GetInstance() falls back to the Go engine.
func TestFindBinary(t *testing.T) {
	if "" != FindBinary("snappymail-definitely-not-installed") {
		t.Error("FindBinary found a nonexistent binary")
	}
}

// TestEngineVersion checks the getEngineInfo()['version'] helper.
func TestEngineVersion(t *testing.T) {
	if "" == EngineVersion() {
		t.Error("EngineVersion is empty")
	}
}
