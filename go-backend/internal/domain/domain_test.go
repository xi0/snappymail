package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewDomainDefaults(t *testing.T) {
	d := New("Example.COM", Defaults{})

	if want := "example.com"; want != d.Name {
		t.Errorf("Name = %q, want %q", d.Name, want)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"IMAP.port", d.IMAP.Port, DefaultIMAPPort},
		{"IMAP.timeout", d.IMAP.Timeout, DefaultIMAPTimeout},
		{"IMAP.type", d.IMAP.Type, SecurityAutoDetect},
		{"IMAP.shortLogin", d.IMAP.ShortLogin, false},
		{"IMAP.lowerLogin", d.IMAP.LowerLogin, true},
		{"IMAP.stripLogin", d.IMAP.StripLogin, ""},
		{"IMAP.fast_simple_search", d.IMAP.FastSimpleSearch, true},
		{"IMAP.message_list_limit", d.IMAP.MessageListLimit, DefaultMessageListLimit},
		{"IMAP.spam_headers", d.IMAP.SpamHeaders, DefaultSpamHeaders},
		{"IMAP.virus_headers", d.IMAP.VirusHeaders, DefaultVirusHeaders},
		{"SMTP.port", d.SMTP.Port, DefaultSMTPPort},
		{"SMTP.timeout", d.SMTP.Timeout, DefaultSMTPTimeout},
		// A fresh Smtp\Settings keeps ConnectSettings' useAuth default.
		{"SMTP.useAuth", d.SMTP.UseAuth, true},
		{"SMTP.authPlainLine", d.SMTP.AuthPlainLine, false},
		{"Sieve.port", d.Sieve.Port, DefaultSievePort},
		{"Sieve.timeout", d.Sieve.Timeout, DefaultSieveTimeout},
		{"Sieve.enabled", d.Sieve.Enabled, false},
		{"Sieve.authLiteral", d.Sieve.AuthLiteral, true},
		// SSLContext reads the [ssl] section of application.ini.
		{"IMAP.ssl.verify_peer", d.IMAP.SSL.VerifyPeer, true},
		{"IMAP.ssl.verify_peer_name", d.IMAP.SSL.VerifyPeerName, true},
		{"IMAP.ssl.disable_compression", d.IMAP.SSL.DisableCompression, true},
		{"IMAP.ssl.security_level", d.IMAP.SSL.SecurityLevel, 1},
		{`IMAP.sasl`, strings.Join(d.IMAP.SASL, " "), strings.Join(defaultSASL, " ")},
		{"WhiteList", d.WhiteList, ""},
		{"AliasName", d.AliasName(), ""},
	}

	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s = %#v, want %#v", test.name, test.got, test.want)
		}
	}

	// The SASL list is copied per settings instance, not shared.
	d.IMAP.SASL[0] = "GSSAPI"
	if "SCRAM-SHA3-512" != d.SMTP.SASL[0] {
		t.Error("the SASL list must not be shared between settings")
	}
}

func TestNewDomainUsesGlobalSSL(t *testing.T) {
	d := New("example.com", Defaults{SSL: &GlobalSSL{
		VerifyCertificate:  false,
		AllowSelfSigned:    true,
		SecurityLevel:      3,
		CAFile:             " /etc/ssl/local.crt ",
		LocalCert:          "/etc/ssl/client.pem",
		DisableCompression: true,
	}})

	ssl := d.IMAP.SSL
	if ssl.VerifyPeer || ssl.VerifyPeerName {
		t.Error("verify_certificate = false must turn both peer checks off")
	}
	if !ssl.AllowSelfSigned || 3 != ssl.SecurityLevel {
		t.Errorf("ssl = %#v, want the configured values", ssl)
	}
	if "/etc/ssl/local.crt" != ssl.CAFile {
		t.Errorf("cafile = %q, want it trimmed", ssl.CAFile)
	}
	if !ssl.DisableCompression {
		t.Error("disable_compression must be read from the [ssl] section")
	}
}

func TestFromArrayReadsTheCanonicalShape(t *testing.T) {
	d := FromArray("SnappyMail.EU", readJSONFixture(t, "default.json"), Defaults{})

	if nil == d {
		t.Fatal("FromArray returned nil")
	}
	if want := "snappymail.eu"; want != d.Name {
		t.Errorf("Name = %q, want %q", d.Name, want)
	}

	if "localhost" != d.IMAP.Host || 143 != d.IMAP.Port || SecurityNone != d.IMAP.Type {
		t.Errorf("IMAP = %#v", d.IMAP.Connect)
	}
	if 300 != d.IMAP.Timeout {
		t.Errorf("IMAP timeout = %d, want 300", d.IMAP.Timeout)
	}
	if false != d.IMAP.SSL.VerifyPeer || true != d.IMAP.SSL.SNIEnabled {
		t.Errorf("IMAP ssl = %#v", d.IMAP.SSL)
	}
	if want := "METADATA OBJECTID PREVIEW STATUS=SIZE"; want != strings.Join(d.IMAP.DisabledCapabilities, " ") {
		t.Errorf("disabled_capabilities = %q, want %q", d.IMAP.DisabledCapabilities, want)
	}

	// SMTP::fromArray() applies !empty() to useAuth, so an absent key is false.
	if d.SMTP.UseAuth {
		t.Error("SMTP.useAuth must be false for the canonical default file")
	}
	if !d.Sieve.AuthLiteral {
		t.Error("Sieve.authLiteral must default to true")
	}
	if "" != d.WhiteList {
		t.Errorf("whiteList = %q, want it empty", d.WhiteList)
	}

	// Without a server block there is no domain.
	if nil != FromArray("example.com", map[string]any{}, Defaults{}) {
		t.Error("FromArray without IMAP must return nil")
	}
	if nil != FromArray("", readJSONFixture(t, "default.json"), Defaults{}) {
		t.Error("FromArray without a name must return nil")
	}
}

func TestFromArrayReadsLegacyFlatKeys(t *testing.T) {
	d := FromArray("example.com", map[string]any{
		"imapHost":          "imap.example.com",
		"imapPort":          "993",
		"imapSecure":        "1",
		"imapShortLogin":    "1",
		"useSieve":          true,
		"sieveHost":         "sieve.example.com",
		"sievePort":         "4190",
		"sieveSecure":       "2",
		"smtpHost":          "smtp.example.com",
		"smtpPort":          "587",
		"smtpSecure":        float64(SecuritySTARTTLS),
		"smtpAuth":          "1",
		"smtpSetSender":     "",
		"smtpAuthPlainLine": "On",
		"whiteList":         "@example.com",
	}, Defaults{})

	if nil == d {
		t.Fatal("FromArray returned nil")
	}

	if "imap.example.com" != d.IMAP.Host || 993 != d.IMAP.Port || SecuritySSL != d.IMAP.Type {
		t.Errorf("IMAP = %#v", d.IMAP.Connect)
	}
	if !d.IMAP.ShortLogin {
		t.Error("imapShortLogin must be read")
	}
	if !d.Sieve.Enabled || "sieve.example.com" != d.Sieve.Host || SecuritySTARTTLS != d.Sieve.Type {
		t.Errorf("Sieve = %#v", d.Sieve)
	}
	if "smtp.example.com" != d.SMTP.Host || 587 != d.SMTP.Port || SecuritySTARTTLS != d.SMTP.Type {
		t.Errorf("SMTP = %#v", d.SMTP.Connect)
	}
	if !d.SMTP.UseAuth || d.SMTP.SetSender || !d.SMTP.AuthPlainLine || d.SMTP.UsePhpMail {
		t.Errorf("SMTP flags = %#v", d.SMTP)
	}
	if want := "@example.com"; want != d.WhiteList {
		t.Errorf("whiteList = %q, want %q", d.WhiteList, want)
	}
}

func TestFromIniArrayReadsTheLegacyIni(t *testing.T) {
	d := FromIniArray("example.com", map[string]string{
		"imap_host":        "imap.example.com",
		"imap_port":        "993",
		"imap_secure":      "SSL",
		"imap_short_login": "1",
		"sieve_use":        "1",
		"sieve_host":       "",
		"sieve_secure":     "TLS",
		"smtp_host":        " smtp.example.com ",
		"smtp_port":        "587",
		"smtp_secure":      "tls",
		"smtp_short_login": "0",
		"smtp_auth":        "1",
		"smtp_set_sender":  "0",
		"smtp_php_mail":    "0",
		"white_list":       " @example.com ",
	}, Defaults{})

	if nil == d {
		t.Fatal("FromIniArray returned nil")
	}

	// A missing port key keeps the class default, an empty one becomes 0 (PHP:
	// `(int) ($aDomain['sieve_port'] ?? 4190)`).
	if 4190 != d.Sieve.Port {
		t.Errorf("Sieve port = %d, want 4190", d.Sieve.Port)
	}
	if SecuritySTARTTLS != d.Sieve.Type {
		t.Errorf("Sieve type = %d, want STARTTLS", d.Sieve.Type)
	}
	if !d.Sieve.Enabled {
		t.Error("sieve_use must enable Sieve")
	}
	if "smtp.example.com" != d.SMTP.Host {
		t.Errorf("SMTP host = %q, want it trimmed", d.SMTP.Host)
	}
	if 587 != d.SMTP.Port || SecuritySTARTTLS != d.SMTP.Type || !d.SMTP.UseAuth {
		t.Errorf("SMTP = %#v", d.SMTP)
	}
	if want := "@example.com"; want != d.WhiteList {
		t.Errorf("whiteList = %q, want %q", d.WhiteList, want)
	}

	// An INI file without imap_host is not a domain config.
	if nil != FromIniArray("example.com", map[string]string{"smtp_host": "x"}, Defaults{}) {
		t.Error("FromIniArray without imap_host must return nil")
	}

	// An empty (but present) port is 0, not the default.
	empty := FromIniArray("example.com", map[string]string{
		"imap_host":  "imap.example.com",
		"sieve_port": "",
		"smtp_port":  "",
	}, Defaults{})
	if nil == empty {
		t.Fatal("FromIniArray returned nil")
	}
	if 0 != empty.Sieve.Port || 0 != empty.SMTP.Port {
		t.Errorf("ports = %d/%d, want 0/0 for present but empty values",
			empty.Sieve.Port, empty.SMTP.Port)
	}
}

func TestValidateWhiteList(t *testing.T) {
	tests := []struct {
		list  string
		email string
		want  bool
	}{
		{"", "someone@example.com", true},
		{"someone@example.com", "someone@example.com", true},
		{"someone@example.com", "other@example.com", false},
		{"someone@example.com", "SOMEONE@Example.COM", true},
		{"someone", "someone@example.com", true},
		{"@example.com", "someone@example.com", true},
		{"@example.com", "someone@example.org", false},
		{"one@example.com; two@example.com", "two@example.com", true},
		{"one@example.com, two@example.com", "two@example.com", true},
		{"one@example.com three@example.com", "three@example.com", true},
		{"user@bücher.example", "user@xn--bcher-kva.example", true},
		{"user@XN--BCHER-KVA.example", "user@bücher.example", true},
		{"*@example.com", "someone@example.com", false},
		// An address without "@" is compared as a whole and as the "domain".
		{"someone", "someone", true},
		{"someone@example.com", "someone", false},
	}

	for _, test := range tests {
		d := New("example.com", Defaults{})
		d.WhiteList = test.list

		if got := d.ValidateWhiteList(test.email); got != test.want {
			t.Errorf("ValidateWhiteList(%q) with list %q = %v, want %v",
				test.email, test.list, got, test.want)
		}
	}
}

func TestFixUsername(t *testing.T) {
	tests := []struct {
		name         string
		connect      Connect
		value        string
		allowShorten bool
		want         string
	}{
		{"plain", Connect{LowerLogin: true}, "User@Example.COM", true, "user@example.com"},
		{"short login", Connect{ShortLogin: true, LowerLogin: true},
			"User@Example.COM", true, "user"},
		{"short login not allowed", Connect{ShortLogin: true, LowerLogin: true},
			"User@Example.COM", false, "user@example.com"},
		{"keep case", Connect{LowerLogin: false}, "User@Example.COM", true, "User@example.com"},
		{"strip login", Connect{LowerLogin: true, StripLogin: ".+"},
			"first.last+tag@example.com", true, "firstlasttag@example.com"},
		{"strip login, no local part", Connect{LowerLogin: true, StripLogin: "-"},
			"user@example.com", true, "user@example.com"},
		{"idn", Connect{LowerLogin: true}, "user@BÜCHER.example", true, "user@xn--bcher-kva.example"},
	}

	for _, test := range tests {
		got := test.connect.FixUsername(test.value, test.allowShorten)
		if got != test.want {
			t.Errorf("%s: FixUsername(%q) = %q, want %q", test.name, test.value, got, test.want)
		}
	}
}

func TestEncodeMatchesPHPPrettyPrint(t *testing.T) {
	d := New("example.com", Defaults{})
	d.WhiteList = "user@bücher.example / \"q\" \\ 😀\t"
	d.IMAP.SASL = nil

	content, err := d.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}

	text := string(content)

	// json_encode() escapes "/" and non-ASCII and uses \uXXXX surrogate pairs.
	if want := `"user@b\u00fccher.example \/ \"q\" \\ \ud83d\ude00\t"`; !strings.Contains(text, want) {
		t.Errorf("whiteList = %q, want %q in the output", text, want)
	}
	// An empty PHP array is [].
	if !strings.Contains(text, `"sasl": []`) {
		t.Errorf("empty sasl must be written as []:\n%s", text)
	}
	if !strings.Contains(text, `"disabled_capabilities": []`) {
		t.Errorf("empty disabled_capabilities must be written as []:\n%s", text)
	}
	// Four spaces per level, ": " between key and value, no trailing newline.
	if !strings.HasPrefix(text, "{\n    \"IMAP\": {\n        \"host\": \"\",\n") {
		t.Errorf("unexpected layout:\n%s", text)
	}
	if strings.HasSuffix(text, "\n") {
		t.Error("json_encode() does not end with a newline")
	}
}

func TestEncodeSortToESortQuirk(t *testing.T) {
	d := New("example.com", Defaults{})
	d.IMAP.DisabledCapabilities = []string{"SORT", "METADATA"}

	content, err := d.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}

	// jsonSerialize() appends ESORT when SORT is disabled.
	want := "\"disabled_capabilities\": [\n            \"SORT\",\n            \"METADATA\",\n            \"ESORT\"\n        ]"
	if !strings.Contains(string(content), want) {
		t.Errorf("output does not contain %q:\n%s", want, content)
	}

	// Serializing twice gives the same output.
	again, err := d.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}
	if string(content) != string(again) {
		t.Error("Encode must be idempotent")
	}
}

func TestEncodeIncludesAliasName(t *testing.T) {
	d := New("alias.example", Defaults{})
	d.SetAliasName("Alias.Example")

	content, err := d.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}
	if want := "\"whiteList\": \"\",\n    \"aliasName\": \"alias.example\""; !strings.Contains(string(content), want) {
		t.Errorf("output does not contain %q:\n%s", want, content)
	}
}

func TestToASCII(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"example.com", "example.com"},
		{"Example.COM", "example.com"},
		// The root label of an absolute name is allowed.
		{"example.com.", "example.com."},
		{"bücher.example", "xn--bcher-kva.example"},
		{"BÜCHER.example", "xn--bcher-kva.example"},
		{"xn--bcher-kva.example", "xn--bcher-kva.example"},
		// The example of SnappyMail\IDN::toAscii().
		{"📧.SnappyMail.EU", "xn--du8h.snappymail.eu"},
		// IDNA_DEFAULT is transitional processing: ß maps to "ss".
		{"straße.example", "strasse.example"},
		// A non-transitional ACE label keeps its deviation character.
		{"xn--strae-oqa.example", "xn--strae-oqa.example"},
		// The wildcard file names must survive (no STD3 rules).
		{"*", "*"},
		{"*.example.com", "*.example.com"},
		{"*example.com", "*example.com"},
		{"_wildcard_example.com", "_wildcard_example.com"},
		{"", ""},
		// UTS46 rejects these.
		{"-example.com", ""},
		{"example-.com", ""},
		{"example..com", ""},
		{".example.com", ""},
		{".example.com.", ""},
		{"ab--cd.example", ""},
		{"xn--9999999999.example", ""},
		{"xn--a.example", ""},
		{strings.Repeat("a", 64) + ".example", ""},
		{strings.Repeat("a", 63) + ".example", strings.Repeat("a", 63) + ".example"},
		{"\xff.example", ""},
		// 253 bytes is the maximum domain length, 254 is one too many.
		{longName(253), longName(253)},
		{longName(254), ""},
	}

	for _, test := range tests {
		if got := toASCII(test.name); got != test.want {
			t.Errorf("toASCII(%q) = %q, want %q", test.name, got, test.want)
		}
	}

	// The length cases above rely on the helper's exactness.
	for _, length := range []int{253, 254} {
		if got := longName(length); length != len(got) {
			t.Errorf("longName(%d) has %d bytes", length, len(got))
		}
	}
}

// longName builds an ASCII domain name of exactly length bytes from labels of
// the maximum size.
func longName(length int) string {
	name := strings.Repeat("a", maxLabelSize)
	for len(name) < length {
		name += "." + strings.Repeat("a", maxLabelSize)
	}
	return name[:length]
}

func TestEmailToASCII(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{"user@Example.COM", "user@example.com"},
		{"User@bücher.example", "User@xn--bcher-kva.example"},
		{"User@straße.example", "User@strasse.example"},
		// Without "@" the value is untouched (SnappyMail\IDN::emailToAscii()).
		{"example.COM", "example.COM"},
		{"user@one@Example.COM", "user@one@example.com"},
		// An invalid domain part ends up empty, like strtolower(false) in PHP.
		{"user@-invalid.example", "user@"},
	}

	for _, test := range tests {
		if got := EmailToASCII(test.address); got != test.want {
			t.Errorf("EmailToASCII(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestEmailToUTF8(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{"user@xn--bcher-kva.example", "user@bücher.example"},
		{"User@XN--BCHER-KVA.example", "User@bücher.example"},
		{"user@xn--du8h.snappymail.eu", "user@📧.snappymail.eu"},
		{"user@example.com", "user@example.com"},
		// Without "@" the value is untouched (SnappyMail\IDN::emailToUtf8()).
		{"xn--bcher-kva.example", "xn--bcher-kva.example"},
	}

	for _, test := range tests {
		if got := EmailToUTF8(test.address); got != test.want {
			t.Errorf("EmailToUTF8(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestLocalPart(t *testing.T) {
	tests := map[string]string{
		"user@example.com": "user",
		"user@one@two.com": "user@one",
		"user":             "user",
		"":                 "",
	}
	for email, want := range tests {
		if got := LocalPart(email); got != want {
			t.Errorf("LocalPart(%q) = %q, want %q", email, got, want)
		}
	}
}

// readJSONFixture parses a checked-in testdata JSON file.
func readJSONFixture(t *testing.T, name string) map[string]any {
	t.Helper()

	file := filepath.Join("testdata", name)
	value := decodeJSONObject(file)
	if nil == value {
		t.Fatalf("cannot parse %s", file)
	}
	return value
}

// readFile returns the content of name.
func readFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(name)
	if nil != err {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}
