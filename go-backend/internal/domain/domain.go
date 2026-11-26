// Package domain implements the domain configuration contract of SnappyMail:
// the RainLoop\Model\Domain model (the JSON shape of a domain config) and the
// RainLoop\Providers\Domain\DefaultDomain provider that stores it below
// data/_data_/<name>/domains/.
//
// A domain config is a compatibility contract, because the PHP backend may run
// against the same data folder at any time, so the observable behaviour is
// ported as-is instead of being "improved":
//
//   - names are kept in lowercase punycode, because an internationalized domain
//     name may be written in uppercase or titlecase;
//   - the file name is the encoded name: "*" becomes "default" and every other
//     "*" becomes "_wildcard_";
//   - Save() writes json_encode($domain, JSON_PRETTY_PRINT): four space indent,
//     escaped "/" and \uXXXX for non-ASCII, and no trailing newline;
//   - GetList() returns the normal domains (sorted ascending), then the aliases
//     (sorted ascending), then the wildcard domains (sorted descending);
//   - Disable() compares the re-read "disabled" file with the in-memory array
//     *including its keys*, so enabling a name that is not the first removed
//     entry reports false even though the file was written correctly.
//
// The JSON in and out of this package is deliberately the PHP encoding rather
// than encoding/json: the file is read by PHP as well.
package domain

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ConnectionSecurityType values (MailSo\Net\Enumerations\ConnectionSecurityType).
const (
	// SecurityNone is a plain connection.
	SecurityNone = 0
	// SecuritySSL is an implicit TLS connection.
	SecuritySSL = 1
	// SecuritySTARTTLS upgrades a plain connection.
	SecuritySTARTTLS = 2
	// SecurityAutoDetect picks SSL for the well known TLS ports.
	SecurityAutoDetect = 9
)

// Port, timeout and limit defaults of MailSo\Imap\Settings,
// MailSo\Smtp\Settings and MailSo\Sieve\Settings.
const (
	DefaultIMAPPort     = 143
	DefaultIMAPTimeout  = 300
	DefaultSMTPPort     = 25
	DefaultSMTPTimeout  = 60
	DefaultSievePort    = 4190
	DefaultSieveTimeout = 10

	DefaultMessageListLimit = 10000
	DefaultSpamHeaders      = "rspamd,spamassassin,bogofilter"
	DefaultVirusHeaders     = "rspamd,clamav"
)

// defaultSASL is the ConnectSettings::$SASLMechanisms default, in preference
// order (see https://github.com/the-djmaze/snappymail/issues/182).
var defaultSASL = []string{
	"SCRAM-SHA3-512",
	"SCRAM-SHA-512",
	"SCRAM-SHA-256",
	"SCRAM-SHA-1",
	"PLAIN",
	"LOGIN",
}

// GlobalSSL is the application.ini [ssl] section, which
// MailSo\Net\SSLContext reads in its constructor to seed a new domain config.
type GlobalSSL struct {
	// VerifyCertificate is ssl.verify_certificate.
	VerifyCertificate bool
	// AllowSelfSigned is ssl.allow_self_signed.
	AllowSelfSigned bool
	// SecurityLevel is ssl.security_level.
	SecurityLevel int
	// CAFile is ssl.cafile.
	CAFile string
	// CAPath is ssl.capath.
	CAPath string
	// LocalCert is ssl.local_cert.
	LocalCert string
	// DisableCompression is ssl.disable_compression.
	DisableCompression bool
}

// DefaultGlobalSSL returns the [ssl] defaults of
// RainLoop\Config\Application::defaultValues().
func DefaultGlobalSSL() GlobalSSL {
	return GlobalSSL{
		VerifyCertificate:  true,
		AllowSelfSigned:    false,
		SecurityLevel:      1,
		DisableCompression: true,
	}
}

// Defaults carries the environment dependent parts of a new domain config.
//
// The zero value is usable and equal to PHP running with an untouched
// application.ini.
type Defaults struct {
	// SSL is the [ssl] section of application.ini. A nil pointer uses
	// DefaultGlobalSSL().
	SSL *GlobalSSL
}

// ssl builds the SSLContext a new domain config starts with.
func (d Defaults) ssl() SSL {
	global := DefaultGlobalSSL()
	if nil != d.SSL {
		global = *d.SSL
	}

	return SSL{
		VerifyPeer:         global.VerifyCertificate,
		VerifyPeerName:     global.VerifyCertificate,
		AllowSelfSigned:    global.AllowSelfSigned,
		CAFile:             strings.TrimSpace(global.CAFile),
		CAPath:             strings.TrimSpace(global.CAPath),
		SNIEnabled:         true,
		DisableCompression: global.DisableCompression,
		SecurityLevel:      global.SecurityLevel,
		LocalCert:          strings.TrimSpace(global.LocalCert),
	}
}

// Domain is RainLoop\Model\Domain: one entry of
// data/_data_/<name>/domains/.
type Domain struct {
	// Name is the lowercase punycode domain name.
	Name string
	// IMAP is the incoming server configuration.
	IMAP IMAPSettings
	// SMTP is the outgoing server configuration.
	SMTP SMTPSettings
	// Sieve is the ManageSieve configuration.
	Sieve SieveSettings
	// WhiteList restricts the accounts this domain may serve (empty = all).
	WhiteList string

	// aliasName is set when the config was loaded through a ".alias" file; it
	// is written to the JSON as "aliasName" like jsonSerialize() does.
	aliasName string
}

// New builds an empty domain config with the defaults of
// MailSo\{Imap,Smtp,Sieve}\Settings and the [ssl] section of application.ini.
func New(name string, defaults Defaults) *Domain {
	return &Domain{
		Name:  toASCII(name),
		IMAP:  newIMAPSettings(defaults),
		SMTP:  newSMTPSettings(defaults),
		Sieve: newSieveSettings(defaults),
	}
}

// AliasName returns the alias this config was loaded through, or "".
func (d *Domain) AliasName() string { return d.aliasName }

// SetAliasName is Domain::SetAliasName(): the alias is stored as lowercase
// punycode as well.
func (d *Domain) SetAliasName(name string) { d.aliasName = toASCII(name) }

// Normalize trims the server hosts and the whitelist, like
// Domain::Normalize() after a load.
func (d *Domain) Normalize() {
	d.IMAP.Host = strings.TrimSpace(d.IMAP.Host)
	d.Sieve.Host = strings.TrimSpace(d.Sieve.Host)
	d.SMTP.Host = strings.TrimSpace(d.SMTP.Host)
	d.WhiteList = strings.TrimSpace(d.WhiteList)
}

// ValidateWhiteList is Domain::ValidateWhiteList(): an entry may match the whole
// address, the local part or "@domain".
func (d *Domain) ValidateWhiteList(email string) bool {
	if "" == d.WhiteList {
		return true
	}

	email = EmailToASCII(strings.ToLower(email))
	// PHP: substr($sEmail, 0, strrpos($sEmail, '@')) and substr($sEmail, $iPos),
	// which for an address without "@" gives "" and the whole address.
	userPart, userDomain := "", email
	if index := strings.LastIndex(email, "@"); 0 <= index {
		userPart, userDomain = email[:index], email[index:]
	}

	for _, item := range strtok(d.WhiteList, " ;,\n") {
		item = EmailToASCII(strings.ToLower(strings.TrimSpace(item)))
		if item == email || item == userPart || item == userDomain {
			return true
		}
	}

	return false
}

// Encode returns the exact bytes Save() writes: json_encode($domain,
// JSON_PRETTY_PRINT).
func (d *Domain) Encode() ([]byte, error) {
	return marshalPretty(d.object())
}

// JSONSerialize is Domain::jsonSerialize(): the domain config as a decoded JSON
// object, the shape Actions\AdminDomains::DoAdminDomainLoad() returns (the
// caller adds the "name").
//
// The value is produced by round-tripping Encode(), so the nested IMAP/SMTP/
// Sieve blocks keep the same key names and types the PHP admin panel reads.
func (d *Domain) JSONSerialize() map[string]any {
	encoded, err := d.Encode()
	if nil != err {
		return map[string]any{}
	}

	var result map[string]any
	if err := json.Unmarshal(encoded, &result); nil != err {
		return map[string]any{}
	}
	return result
}

// object is Domain::jsonSerialize().
func (d *Domain) object() object {
	result := object{
		{"IMAP", d.IMAP.object()},
		{"SMTP", d.SMTP.object()},
		{"Sieve", d.Sieve.object()},
		{"whiteList", d.WhiteList},
	}
	if "" != d.aliasName {
		result = append(result, field{"aliasName", d.aliasName})
	}
	return result
}

// FromArray is Domain::fromArray(): the canonical "<name>.json" shape, or nil
// when the name or the server block is missing.
func FromArray(name string, value map[string]any, defaults Defaults) *Domain {
	if "" == name {
		return nil
	}

	result := New(name, defaults)

	if truthy(value["IMAP"]) {
		result.IMAP = imapSettingsFromArray(value["IMAP"], result.IMAP)
		result.SMTP = smtpSettingsFromArray(value["SMTP"], result.SMTP)
		result.Sieve = sieveSettingsFromArray(value["Sieve"], result.Sieve)
		result.WhiteList = phpString(value["whiteList"])
	} else if "" != phpString(value["imapHost"]) {
		// The layout of older RainLoop versions, which stored the settings
		// flat in the file.
		result.IMAP.Host = phpString(value["imapHost"])
		result.IMAP.Port = phpInt(value["imapPort"])
		result.IMAP.Type = phpInt(value["imapSecure"])
		result.IMAP.ShortLogin = truthy(value["imapShortLogin"])

		result.Sieve.Enabled = truthy(value["useSieve"])
		result.Sieve.Host = phpString(value["sieveHost"])
		result.Sieve.Port = phpInt(value["sievePort"])
		result.Sieve.Type = phpInt(value["sieveSecure"])

		result.SMTP.Host = phpString(value["smtpHost"])
		result.SMTP.Port = phpInt(value["smtpPort"])
		result.SMTP.Type = phpInt(value["smtpSecure"])
		result.SMTP.ShortLogin = truthy(value["smtpShortLogin"])
		result.SMTP.UseAuth = truthy(value["smtpAuth"])
		result.SMTP.SetSender = truthy(value["smtpSetSender"])
		result.SMTP.AuthPlainLine = truthy(value["smtpAuthPlainLine"])
		result.SMTP.UsePhpMail = truthy(value["smtpPhpMail"])

		result.WhiteList = phpString(value["whiteList"])
	} else {
		return nil
	}

	result.Normalize()

	return result
}

// FromIniArray is Domain::fromIniArray(), the reader of the legacy
// "<name>.ini" files written by older RainLoop versions.
func FromIniArray(name string, value map[string]string, defaults Defaults) *Domain {
	if "" == name || "" == value["imap_host"] {
		return nil
	}

	result := New(name, defaults)

	result.IMAP.Host = value["imap_host"]
	result.IMAP.Port = phpInt(value["imap_port"])
	result.IMAP.Type = securityTypeFromName(value["imap_secure"])
	result.IMAP.ShortLogin = truthy(value["imap_short_login"])

	result.Sieve.Enabled = truthy(value["sieve_use"])
	result.Sieve.Host = value["sieve_host"]
	// PHP: (int) ($aDomain['sieve_port'] ?? 4190), so a *missing* key keeps the
	// port default while an empty value becomes 0.
	if port, ok := value["sieve_port"]; ok {
		result.Sieve.Port = phpInt(port)
	}
	result.Sieve.Type = securityTypeFromName(value["sieve_secure"])

	result.SMTP.Host = value["smtp_host"]
	if port, ok := value["smtp_port"]; ok {
		result.SMTP.Port = phpInt(port)
	}
	result.SMTP.Type = securityTypeFromName(value["smtp_secure"])
	result.SMTP.ShortLogin = truthy(value["smtp_short_login"])
	result.SMTP.UseAuth = truthy(value["smtp_auth"])
	result.SMTP.SetSender = truthy(value["smtp_set_sender"])
	result.SMTP.UsePhpMail = truthy(value["smtp_php_mail"])

	result.WhiteList = value["white_list"]
	result.Normalize()

	return result
}

// securityTypeFromName is Domain::StrConnectionSecurityTypeToCons().
func securityTypeFromName(name string) int {
	switch strings.ToUpper(name) {
	case "SSL":
		return SecuritySSL
	case "TLS":
		return SecuritySTARTTLS
	}
	return SecurityNone
}

// strtok splits value on any of the separator characters and drops the empty
// fragments, like PHP's strtok() loop over a character list.
func strtok(value, separators string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return strings.ContainsRune(separators, r)
	})
}

// truthy ports PHP's !empty(): "" and "0" are empty strings, 0 is an empty
// number, nil an empty value and an empty array is empty as well.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return "" != typed && "0" != typed
	case int:
		return 0 != typed
	case int64:
		return 0 != typed
	case float64:
		return 0 != typed
	case []any:
		return 0 < len(typed)
	case []string:
		return 0 < len(typed)
	case map[string]any:
		return 0 < len(typed)
	}
	return true
}

// phpString ports PHP's (string) cast for the types a JSON or INI value can
// have.
func phpString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}
		return ""
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	}
	return ""
}

// phpInt ports PHP's (int) cast: a leading number wins, everything else is 0.
func phpInt(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case json.Number:
		if number, err := typed.Int64(); nil == err {
			return int(number)
		}
		if number, err := typed.Float64(); nil == err {
			return int(number)
		}
		return 0
	case string:
		trimmed := strings.TrimSpace(typed)
		if number, err := strconv.Atoi(trimmed); nil == err {
			return number
		}
		if number, err := strconv.ParseFloat(trimmed, 64); nil == err {
			return int(number)
		}
		// "1.5abc" and "20 messages" start with a number, like PHP's cast.
		if end := numericPrefix(trimmed); 0 < end {
			if number, err := strconv.ParseFloat(trimmed[:end], 64); nil == err {
				return int(number)
			}
		}
		return 0
	}
	return 0
}

// numericPrefix returns the length of the leading PHP numeric part of value.
func numericPrefix(value string) int {
	end := 0
	for end < len(value) {
		character := value[end]
		if ('0' <= character && '9' >= character) || '+' == character || '-' == character ||
			'.' == character || 'e' == character || 'E' == character {
			end++
			continue
		}
		break
	}
	return end
}

// anyMap returns value as a string keyed map, or nil.
func anyMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

// stringList returns value as a list of strings; ok is false when PHP would
// ignore it (!empty() and is_array()).
func stringList(value any) ([]string, bool) {
	var items []any
	switch typed := value.(type) {
	case []any:
		items = typed
	case []string:
		if 0 == len(typed) {
			return nil, false
		}
		return typed, true
	default:
		return nil, false
	}
	if 0 == len(items) {
		return nil, false
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, phpString(item))
	}
	return result, true
}
