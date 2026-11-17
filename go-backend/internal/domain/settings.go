package domain

import "strings"

// SSL is MailSo\Net\SSLContext: the client TLS settings of one connection.
type SSL struct {
	// VerifyPeer is "verify_peer": require a valid certificate.
	VerifyPeer bool
	// VerifyPeerName checks the certificate host name.
	VerifyPeerName bool
	// AllowSelfSigned accepts self signed certificates.
	AllowSelfSigned bool
	// CAFile is the certificate authority bundle on the local filesystem.
	CAFile string
	// CAPath is a hashed certificate directory.
	CAPath string
	// SNIEnabled sends the TLS server name.
	SNIEnabled bool
	// DisableCompression turns TLS compression off (CRIME).
	DisableCompression bool
	// SecurityLevel is the OpenSSL security level.
	SecurityLevel int
	// LocalCert is a client certificate (pem with private key).
	LocalCert string
}

// object is SSLContext::jsonSerialize(), which drops the empty strings.
func (s SSL) object() object {
	result := object{
		{"verify_peer", s.VerifyPeer},
		{"verify_peer_name", s.VerifyPeerName},
		{"allow_self_signed", s.AllowSelfSigned},
	}
	if "" != s.CAFile {
		result = append(result, field{"cafile", s.CAFile})
	}
	if "" != s.CAPath {
		result = append(result, field{"capath", s.CAPath})
	}
	return append(result,
		field{"SNI_enabled", s.SNIEnabled},
		field{"disable_compression", s.DisableCompression},
		field{"security_level", s.SecurityLevel},
	)
}

// sslFromArray is SSLContext::fromArray(): every key of the settings object is
// assigned to the property with that name.
func sslFromArray(value any, base SSL) SSL {
	settings := anyMap(value)
	if nil == settings {
		return base
	}

	for key, raw := range settings {
		switch key {
		case "verify_peer":
			base.VerifyPeer = truthy(raw)
		case "verify_peer_name":
			base.VerifyPeerName = truthy(raw)
		case "allow_self_signed":
			base.AllowSelfSigned = truthy(raw)
		case "cafile":
			base.CAFile = phpString(raw)
		case "capath":
			base.CAPath = phpString(raw)
		case "SNI_enabled":
			base.SNIEnabled = truthy(raw)
		case "disable_compression":
			base.DisableCompression = truthy(raw)
		case "security_level":
			base.SecurityLevel = phpInt(raw)
		case "local_cert":
			base.LocalCert = phpString(raw)
		}
	}

	return base
}

// Connect is MailSo\Net\ConnectSettings, the part every server type shares.
type Connect struct {
	// Host is the server host name.
	Host string
	// Port is the server port.
	Port int
	// Timeout is the stream timeout in seconds.
	Timeout int
	// Type is one of the Security* values.
	Type int
	// ShortLogin strips the domain part of the login name.
	ShortLogin bool
	// LowerLogin lowercases the login name.
	LowerLogin bool
	// StripLogin lists characters to remove from the login local part.
	StripLogin string
	// SASL is the SASL mechanism preference list.
	SASL []string
	// SSL is the TLS configuration.
	SSL SSL
	// UseAuth enables authentication. ConnectSettings defaults it to true but
	// does not serialize it, so it is not part of the JSON shape.
	UseAuth bool
}

// newConnect builds the shared part of a server configuration.
func newConnect(defaults Defaults, port, timeout int) Connect {
	return Connect{
		Port:       port,
		Timeout:    timeout,
		Type:       SecurityAutoDetect,
		LowerLogin: true,
		SASL:       append([]string(nil), defaultSASL...),
		SSL:        defaults.ssl(),
		UseAuth:    true,
	}
}

// object is ConnectSettings::jsonSerialize().
func (c Connect) object() object {
	sasl := c.SASL
	if nil == sasl {
		// json_encode() writes an empty PHP array as [].
		sasl = []string{}
	}
	return object{
		{"host", c.Host},
		{"port", c.Port},
		{"type", c.Type},
		{"timeout", c.Timeout},
		{"shortLogin", c.ShortLogin},
		{"lowerLogin", c.LowerLogin},
		{"stripLogin", c.StripLogin},
		{"sasl", sasl},
		{"ssl", c.SSL.object()},
	}
}

// connectFromArray is ConnectSettings::fromArray().
func connectFromArray(value any, base Connect) Connect {
	settings := anyMap(value)
	if nil == settings {
		return base
	}

	if host, ok := settings["host"]; ok {
		base.Host = phpString(host)
	}
	if port, ok := settings["port"]; ok {
		base.Port = phpInt(port)
	}
	if securityType, ok := settings["type"]; ok {
		base.Type = phpInt(securityType)
	} else if secure, ok := settings["secure"]; ok {
		base.Type = phpInt(secure)
	}
	if timeout, ok := settings["timeout"]; ok {
		base.Timeout = phpInt(timeout)
	}
	base.ShortLogin = truthy(settings["shortLogin"])
	if lowerLogin, ok := settings["lowerLogin"]; ok {
		base.LowerLogin = truthy(lowerLogin)
	}
	if stripLogin, ok := settings["stripLogin"]; ok {
		base.StripLogin = phpString(stripLogin)
	}
	base.SSL = sslFromArray(settings["ssl"], base.SSL)
	if sasl, ok := stringList(settings["sasl"]); ok {
		base.SASL = sasl
	}

	return base
}

// FixUsername is ConnectSettings::fixUsername(): convert the domain part to
// punycode, optionally shorten to the local part, lowercase and strip the
// configured characters.
func (c Connect) FixUsername(value string, allowShorten bool) string {
	value = EmailToASCII(value)

	if c.ShortLogin && allowShorten {
		value = LocalPart(value)
	}

	if c.LowerLogin {
		value = strings.ToLower(value)
	}

	if "" != c.StripLogin {
		parts := strings.Split(value, "@")
		parts[0] = strings.Map(func(r rune) rune {
			if strings.ContainsRune(c.StripLogin, r) {
				return -1
			}
			return r
		}, parts[0])
		value = strings.Join(parts, "@")
	}

	return value
}

// LocalPart returns the part before the last "@", like
// MailSo\Base\Utils::getEmailAddressLocalPart().
func LocalPart(email string) string {
	if index := strings.LastIndex(email, "@"); 0 <= index {
		return email[:index]
	}
	return email
}

// IMAPSettings is MailSo\Imap\Settings.
type IMAPSettings struct {
	Connect

	// ExpungeAllOnDelete is written as "use_expunge_all_on_delete".
	ExpungeAllOnDelete bool
	// FastSimpleSearch uses the cheap search when possible.
	FastSimpleSearch bool
	// ForceSelect issues SELECT instead of EXAMINE (fixes servers without
	// read-only support).
	ForceSelect bool
	// MessageAllHeaders fetches every header of a message.
	MessageAllHeaders bool
	// MessageListLimit limits the number of listed messages.
	MessageListLimit int
	// SearchFilter is a server side search filter.
	SearchFilter string
	// SpamHeaders lists the headers that mark a message as spam.
	SpamHeaders string
	// VirusHeaders lists the headers that mark a message as a virus.
	VirusHeaders string
	// DisabledCapabilities drops capabilities the server advertises anyway.
	DisabledCapabilities []string
	// FetchNewMessages is imap.fetch_new_messages. It is not serialized (see
	// MailSo\Imap\Settings::jsonSerialize()).
	FetchNewMessages bool
}

// newIMAPSettings builds an IMAP configuration with the MailSo defaults.
func newIMAPSettings(defaults Defaults) IMAPSettings {
	return IMAPSettings{
		Connect:          newConnect(defaults, DefaultIMAPPort, DefaultIMAPTimeout),
		FastSimpleSearch: true,
		MessageListLimit: DefaultMessageListLimit,
		SpamHeaders:      DefaultSpamHeaders,
		VirusHeaders:     DefaultVirusHeaders,
		FetchNewMessages: true,
	}
}

// object is Imap\Settings::jsonSerialize().
func (i IMAPSettings) object() object {
	result := i.Connect.object()
	return append(result,
		field{"use_expunge_all_on_delete", i.ExpungeAllOnDelete},
		field{"fast_simple_search", i.FastSimpleSearch},
		field{"force_select", i.ForceSelect},
		field{"message_all_headers", i.MessageAllHeaders},
		field{"message_list_limit", i.MessageListLimit},
		field{"search_filter", i.SearchFilter},
		field{"spam_headers", i.SpamHeaders},
		field{"virus_headers", i.VirusHeaders},
		field{"disabled_capabilities", i.capabilities()},
	)
}

// capabilities is the disabled_capabilities serialization, including the
// "SORT implies ESORT" rewrite of Imap\Settings::jsonSerialize().
func (i IMAPSettings) capabilities() []string {
	for _, capability := range i.DisabledCapabilities {
		if "SORT" == capability {
			i.DisabledCapabilities = append(i.DisabledCapabilities, "ESORT")
			break
		}
	}
	return unique(i.DisabledCapabilities)
}

// imapSettingsFromArray is Imap\Settings::fromArray().
func imapSettingsFromArray(value any, base IMAPSettings) IMAPSettings {
	settings := anyMap(value)
	if nil == settings {
		return base
	}

	base.Connect = connectFromArray(settings, base.Connect)

	for _, key := range []string{
		"expunge_all_on_delete",
		"fast_simple_search",
		"fetch_new_messages",
		"force_select",
		"message_all_headers",
	} {
		if raw, ok := settings[key]; ok {
			switch key {
			case "expunge_all_on_delete":
				base.ExpungeAllOnDelete = truthy(raw)
			case "fast_simple_search":
				base.FastSimpleSearch = truthy(raw)
			case "fetch_new_messages":
				base.FetchNewMessages = truthy(raw)
			case "force_select":
				base.ForceSelect = truthy(raw)
			case "message_all_headers":
				base.MessageAllHeaders = truthy(raw)
			}
		}
	}

	if raw, ok := settings["message_list_limit"]; ok {
		base.MessageListLimit = phpInt(raw)
	}

	for _, key := range []string{"search_filter", "spam_headers", "virus_headers"} {
		if raw, ok := settings[key]; ok {
			switch key {
			case "search_filter":
				base.SearchFilter = phpString(raw)
			case "spam_headers":
				base.SpamHeaders = phpString(raw)
			case "virus_headers":
				base.VirusHeaders = phpString(raw)
			}
		}
	}

	if capabilities, ok := stringList(settings["disabled_capabilities"]); ok {
		base.DisabledCapabilities = capabilities
	}

	// The old individual disable_* switches map onto capabilities.
	for _, legacy := range []struct{ key, capability string }{
		{"disable_list_status", "list-status"},
		{"disable_metadata", "METADATA"},
		{"disable_move", "MOVE"},
		{"disable_sort", "SORT"},
		{"disable_thread", "THREAD"},
		{"disable_binary", "BINARY"},
		{"disable_status_size", "STATUS=SIZE"},
		{"disable_preview", "PREVIEW"},
	} {
		if truthy(settings[legacy.key]) {
			base.DisabledCapabilities = append(base.DisabledCapabilities, legacy.capability)
		}
	}
	base.DisabledCapabilities = unique(base.DisabledCapabilities)

	return base
}

// SMTPSettings is MailSo\Smtp\Settings.
type SMTPSettings struct {
	Connect

	// SetSender passes the sender with MAIL FROM.
	SetSender bool
	// UsePhpMail sends with PHP's mail() instead of SMTP.
	UsePhpMail bool
	// AuthPlainLine sends AUTH PLAIN credentials on a single line
	// (https://github.com/the-djmaze/snappymail/issues/1038).
	AuthPlainLine bool
}

// newSMTPSettings builds an SMTP configuration with the MailSo defaults.
func newSMTPSettings(defaults Defaults) SMTPSettings {
	settings := SMTPSettings{Connect: newConnect(defaults, DefaultSMTPPort, DefaultSMTPTimeout)}
	// A new Smtp\Settings keeps ConnectSettings' useAuth, while
	// Smtp\Settings::fromArray() always applies !empty($aSettings['useAuth']).
	settings.UseAuth = true
	return settings
}

// object is Smtp\Settings::jsonSerialize().
func (s SMTPSettings) object() object {
	return append(s.Connect.object(),
		field{"useAuth", s.UseAuth},
		field{"setSender", s.SetSender},
		field{"usePhpMail", s.UsePhpMail},
		field{"authPlainLine", s.AuthPlainLine},
	)
}

// smtpSettingsFromArray is Smtp\Settings::fromArray().
func smtpSettingsFromArray(value any, base SMTPSettings) SMTPSettings {
	settings := anyMap(value)
	if nil == settings {
		return base
	}

	base.Connect = connectFromArray(settings, base.Connect)
	base.UseAuth = truthy(settings["useAuth"])
	base.SetSender = truthy(settings["setSender"])
	base.UsePhpMail = truthy(settings["usePhpMail"])
	base.AuthPlainLine = truthy(settings["authPlainLine"])

	return base
}

// SieveSettings is MailSo\Sieve\Settings.
type SieveSettings struct {
	Connect

	// Enabled reports whether the domain uses ManageSieve.
	Enabled bool
	// AuthLiteral sends the credentials as a literal.
	AuthLiteral bool
}

// newSieveSettings builds a ManageSieve configuration with the MailSo defaults.
func newSieveSettings(defaults Defaults) SieveSettings {
	return SieveSettings{
		Connect:     newConnect(defaults, DefaultSievePort, DefaultSieveTimeout),
		AuthLiteral: true,
	}
}

// object is Sieve\Settings::jsonSerialize().
func (s SieveSettings) object() object {
	return append(s.Connect.object(),
		field{"enabled", s.Enabled},
		field{"authLiteral", s.AuthLiteral},
	)
}

// sieveSettingsFromArray is Sieve\Settings::fromArray().
func sieveSettingsFromArray(value any, base SieveSettings) SieveSettings {
	settings := anyMap(value)
	if nil == settings {
		return base
	}

	base.Connect = connectFromArray(settings, base.Connect)
	base.Enabled = truthy(settings["enabled"])
	// authLiteral defaults to true when the key is absent.
	base.AuthLiteral = true
	if raw, ok := settings["authLiteral"]; ok {
		base.AuthLiteral = truthy(raw)
	}

	return base
}

// unique is array_unique(): the first occurrence of a value wins, the order is
// kept.
func unique(values []string) []string {
	if 0 == len(values) {
		return values
	}

	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
