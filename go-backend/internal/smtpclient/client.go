// Package smtpclient ports the sending side of MailSo\Smtp\SmtpClient and
// RainLoop\Model\Account::SmtpConnectAndLogin(): implicit TLS / STARTTLS, the
// EHLO capability negotiation, SASL PLAIN/LOGIN/CRAM-MD5 and the
// MAIL FROM/RCPT TO/DATA sequence DoSendMessage needs.
//
// Only the plain SMTP path is implemented. SMTP.usePhpMail has no Go equivalent
// (PHP's mail() is a local MTA handoff), so it is reported as Unsupported and the
// action returns CantSendMessage, as documented for the caldav mail step.
package smtpclient

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// ErrorKind selects the notification code the action reports.
type ErrorKind int

const (
	// KindConnection is MailSo\Net\Exceptions\ConnectionException.
	KindConnection ErrorKind = iota
	// KindLogin is MailSo\Smtp\Exceptions\LoginException.
	KindLogin
	// KindSize is the RFC 1870 size check of smtpSendMessage().
	KindSize
	// KindUnsupported is the usePhpMail branch.
	KindUnsupported
	// KindOther is any other SMTP failure.
	KindOther
)

// Error is the typed SMTP failure. ViewErrors mirrors
// labs.smtp_show_server_errors (Smtp\Settings::viewErrors).
type Error struct {
	Kind       ErrorKind
	Err        error
	Message    string
	ViewErrors bool
}

// Error implements error.
func (e *Error) Error() string {
	if nil != e.Err {
		return e.Err.Error()
	}
	return e.Message
}

// Unwrap exposes the underlying network/protocol error.
func (e *Error) Unwrap() error { return e.Err }

// Sender sends composed messages through the SMTP configuration of an account.
// It is the *smtpclient.Sender the handler wraps in its Sender interface.
type Sender struct {
	hooks      account.Hooks
	logger     *logx.Logger
	viewErrors bool
}

// NewSender builds the SMTP sender of one request/dispatcher.
func NewSender(hooks account.Hooks, logger *logx.Logger, viewErrors bool) *Sender {
	return &Sender{hooks: hooks, logger: logger, viewErrors: viewErrors}
}

// Send is Actions\Messages::smtpSendMessage() minus the plugin stream hooks,
// which the action runs because it owns the message stream.
func (s *Sender) Send(
	target *account.Account,
	from string,
	recipients []string,
	hidden []string,
	data []byte,
	dsn bool,
	requireTLS bool,
) error {
	if nil == target || nil == target.Domain() {
		return &Error{Kind: KindOther, Message: "account has no domain configuration", ViewErrors: s.viewErrors}
	}

	settings := target.Domain().SMTP

	if settings.UsePhpMail {
		return &Error{Kind: KindUnsupported, Message: "PHP mail() is not available", ViewErrors: s.viewErrors}
	}

	client := New(&settings, s.logger)
	s.runHook("smtp.before-connect", target, client, &settings)
	if err := client.Connect(); nil != err {
		return s.wrap(KindConnection, err)
	}
	s.runHook("smtp.after-connect", target, client, &settings)

	password := target.SmtpPass()
	if !target.HasSmtpPass() {
		password = target.ImapPass()
	}
	s.runHook("smtp.before-login", target, client, &settings)
	var loginErr error
	if settings.UseAuth {
		loginErr = client.Login(target.SmtpUser(), password)
	}
	s.runHook("smtp.after-login", target, client, nil == loginErr, &settings)
	if nil != loginErr {
		_ = client.Close()
		return s.wrap(KindLogin, loginErr)
	}

	if 0 < len(data) && 0 < client.MaxSize() && float64(len(data))*1.33 > float64(client.MaxSize()) {
		_ = client.Close()
		return &Error{
			Kind:       KindSize,
			Message:    "Message size " + strconv.Itoa(int(float64(len(data))*1.33)) + " bigger then max " + strconv.Itoa(client.MaxSize()),
			ViewErrors: s.viewErrors,
		}
	}

	// PHP passes 0 here; the RFC 1870 SIZE limit is checked explicitly above.
	if err := client.MailFrom(from, 0, dsn, requireTLS); nil != err {
		_ = client.Close()
		return s.wrap(KindOther, err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient, dsn); nil != err {
			_ = client.Close()
			return s.wrap(KindOther, err)
		}
	}
	for _, recipient := range hidden {
		if !isEmail(recipient) {
			continue
		}
		if err := client.Rcpt(recipient, false); nil != err {
			_ = client.Close()
			return s.wrap(KindOther, err)
		}
	}
	if err := client.Data(data); nil != err {
		_ = client.Close()
		return s.wrap(KindOther, err)
	}

	if err := client.Disconnect(); nil != err {
		return s.wrap(KindOther, err)
	}
	return nil
}

// wrap attaches the notification kind and the viewErrors flag.
func (s *Sender) wrap(kind ErrorKind, err error) error {
	return &Error{Kind: kind, Err: err, ViewErrors: s.viewErrors}
}

// runHook runs an account connect/login hook when plugins are configured.
func (s *Sender) runHook(name string, args ...any) {
	if nil != s && nil != s.hooks {
		s.hooks.RunHook(name, args...)
	}
}

// isEmail is the hidden recipient regex of smtpSendMessage().
func isEmail(value string) bool {
	at := strings.IndexByte(value, '@')
	if 1 > at || at == len(value)-1 || 1 != strings.Count(value, "@") {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n")
}

// Client is MailSo\Smtp\SmtpClient.
type Client struct {
	Settings *domain.SMTPSettings

	logger *logx.Logger
	conn   net.Conn
	reader *bufio.Reader

	readTimeout time.Duration
	encrypted   bool
	loggedIn    bool
	mailSent    bool
	rcptSent    bool

	capabilities []string
	authTypes    []string
	sizeValue    int
}

// New builds an unconnected client. logger may be nil.
func New(settings *domain.SMTPSettings, logger *logx.Logger) *Client {
	return &Client{Settings: settings, logger: logger}
}

// Connect is SmtpClient::Connect(): greeting, EHLO/HELO, optional STARTTLS.
func (c *Client) Connect() error {
	if nil != c.conn {
		return nil
	}
	if nil == c.Settings {
		return errors.New("smtpclient: missing settings")
	}

	host := strings.TrimSpace(c.Settings.Host)
	if "" == host || 0 > c.Settings.Port || 65535 < c.Settings.Port {
		return connectionError(errors.New("smtpclient: invalid host or port"))
	}

	c.readTimeout = readTimeout(c.Settings.Timeout)
	address := net.JoinHostPort(host, strconv.Itoa(c.Settings.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	implicitTLS := useSSL(c.Settings.Port, c.Settings.Type)
	if implicitTLS {
		tlsConfig, err := tlsConfiguration(&c.Settings.SSL, host)
		if nil != err {
			return connectionError(err)
		}
		raw, err := dialer.Dial("tcp", address)
		if nil != err {
			return connectionError(err)
		}
		secured := tls.Client(raw, tlsConfig)
		if err := c.handshake(secured); nil != err {
			_ = raw.Close()
			return connectionError(err)
		}
		c.conn = secured
		c.encrypted = true
	} else {
		raw, err := dialer.Dial("tcp", address)
		if nil != err {
			return connectionError(err)
		}
		c.conn = raw
	}
	c.reader = bufio.NewReader(c.conn)

	if _, err := c.readResponse(220); nil != err {
		_ = c.Close()
		return err
	}

	if err := c.ehloOrHelo(c.ehloHost()); nil != err {
		_ = c.Close()
		return err
	}

	if domain.SecuritySTARTTLS == c.Settings.Type ||
		(domain.SecurityAutoDetect == c.Settings.Type && c.HasCapability("STARTTLS")) {
		if err := c.startTLS(); nil != err {
			_ = c.Close()
			return err
		}
	}
	return nil
}

// handshake runs the TLS handshake under the read timeout.
func (c *Client) handshake(connection *tls.Conn) error {
	if 0 < c.readTimeout {
		_ = connection.SetDeadline(time.Now().Add(c.readTimeout))
	}
	return connection.Handshake()
}

// startTLS is SmtpClient::StartTLS().
func (c *Client) startTLS() error {
	if !c.HasCapability("STARTTLS") {
		return connectionError(errors.New("smtpclient: STARTTLS is not supported"))
	}
	if _, err := c.command("STARTTLS", 220); nil != err {
		return err
	}

	tlsConfig, err := tlsConfiguration(&c.Settings.SSL, strings.TrimSpace(c.Settings.Host))
	if nil != err {
		return connectionError(err)
	}
	secured := tls.Client(c.conn, tlsConfig)
	if err := c.handshake(secured); nil != err {
		return connectionError(err)
	}
	c.conn = secured
	c.reader = bufio.NewReader(secured)
	c.encrypted = true

	return c.ehloOrHelo(c.ehloHost())
}

// ehloHost is SmtpClient::EhloHelper() for the connection (the Sender resolves
// the same value before it hands the settings over).
func (c *Client) ehloHost() string {
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if "" == host {
		host = "localhost"
	}
	return host
}

// Login is SmtpClient::Login(): the first configured, advertised and supported
// SASL mechanism.
func (c *Client) Login(username, password string) error {
	if c.loggedIn {
		return nil
	}

	mechanism := c.selectMechanism()
	if "" == mechanism {
		if !c.encrypted && c.HasCapability("STARTTLS") {
			if err := c.startTLS(); nil != err {
				return err
			}
			return c.Login(username, password)
		}
		return loginError(errors.New("smtpclient: no supported AUTH mechanism"))
	}

	switch mechanism {
	case "PLAIN":
		return c.authPlain(username, password)
	case "LOGIN":
		return c.authLogin(username, password)
	case "CRAM-MD5":
		return c.authCramMD5(username, password)
	}
	return loginError(fmt.Errorf("smtpclient: unsupported SASL mechanism %q", mechanism))
}

// selectMechanism is the SASL preference loop of SmtpClient::Login().
func (c *Client) selectMechanism() string {
	if nil == c.Settings {
		return ""
	}
	for _, candidate := range c.Settings.SASL {
		upper := strings.ToUpper(strings.TrimSpace(candidate))
		if !supports(upper) {
			continue
		}
		for _, advertised := range c.authTypes {
			if upper == advertised {
				return upper
			}
		}
	}
	return ""
}

// supports reports whether this client implements the SASL mechanism.
func supports(mechanism string) bool {
	return "PLAIN" == mechanism || "LOGIN" == mechanism || "CRAM-MD5" == mechanism
}

// authPlain is the AUTH PLAIN exchange, honouring authPlainLine.
func (c *Client) authPlain(username, password string) error {
	initial := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + password))
	if c.Settings.AuthPlainLine {
		_, err := c.command("AUTH PLAIN "+initial, 235)
		if nil != err {
			return loginError(err)
		}
		c.loggedIn = true
		return nil
	}

	if _, err := c.command("AUTH PLAIN", 334); nil != err {
		return loginError(err)
	}
	if _, err := c.command(initial, 235); nil != err {
		return loginError(err)
	}
	c.loggedIn = true
	return nil
}

// authLogin is the AUTH LOGIN base64 challenge/response exchange.
func (c *Client) authLogin(username, password string) error {
	if _, err := c.command("AUTH LOGIN", 334); nil != err {
		return loginError(err)
	}
	if _, err := c.command(base64.StdEncoding.EncodeToString([]byte(username)), 334); nil != err {
		return loginError(err)
	}
	if _, err := c.command(base64.StdEncoding.EncodeToString([]byte(password)), 235); nil != err {
		return loginError(err)
	}
	c.loggedIn = true
	return nil
}

// authCramMD5 is the AUTH CRAM-MD5 HMAC-MD5 challenge/response exchange.
func (c *Client) authCramMD5(username, password string) error {
	challenge, err := c.command("AUTH CRAM-MD5", 334)
	if nil != err {
		return loginError(err)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(challenge))
	if nil != err {
		return loginError(err)
	}
	mac := hmac.New(md5.New, []byte(password))
	mac.Write(decoded)
	response := base64.StdEncoding.EncodeToString([]byte(username + " " + fmt.Sprintf("%x", mac.Sum(nil))))

	if _, err := c.command(response, 235); nil != err {
		return loginError(err)
	}
	c.loggedIn = true
	return nil
}

// MailFrom is SmtpClient::MailFrom().
func (c *Client) MailFrom(from string, size int, dsn, requireTLS bool) error {
	command := "MAIL FROM:<" + from + ">"
	if 0 < size && c.HasCapability("SIZE") {
		command += " SIZE=" + strconv.Itoa(size)
	}
	if dsn && c.HasCapability("DSN") {
		command += " RET=HDRS"
	}
	if requireTLS && c.HasCapability("REQUIRETLS") {
		command += " REQUIRETLS"
	}

	if _, err := c.command(command, 250); nil != err {
		return err
	}
	c.mailSent = true
	c.rcptSent = false
	return nil
}

// Rcpt is SmtpClient::Rcpt().
func (c *Client) Rcpt(recipient string, dsn bool) error {
	if !c.mailSent {
		return errors.New("smtpclient: no sender reverse path has been supplied")
	}

	command := "RCPT TO:<" + recipient + ">"
	if dsn && c.HasCapability("DSN") {
		command += " NOTIFY=SUCCESS,FAILURE"
	}
	if _, err := c.commandAny(command, []int{250, 251}); nil != err {
		return fmt.Errorf("Failed to add recipient '%s': %w", recipient, err)
	}
	c.rcptSent = true
	return nil
}

// Data is SmtpClient::DataWithStream() for an in-memory message.
func (c *Client) Data(data []byte) error {
	if !c.rcptSent {
		return errors.New("smtpclient: no recipient forward path has been supplied")
	}
	if _, err := c.command("DATA", 354); nil != err {
		return err
	}

	body := normalizeCRLF(string(data))
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}

	c.setDeadline()
	reader := bufio.NewReader(strings.NewReader(body))
	for {
		line, readErr := reader.ReadString('\n')
		if "" != line {
			if strings.HasPrefix(line, ".") {
				line = "." + line
			}
			if _, err := io.WriteString(c.conn, line); nil != err {
				return connectionError(err)
			}
		}
		if nil != readErr {
			break
		}
	}
	if err := c.writeLine("."); nil != err {
		return err
	}

	_, err := c.readResponse(250)
	return err
}

// MaxSize is SmtpClient::maxSize().
func (c *Client) MaxSize() int { return c.sizeValue }

// Capability is SmtpClient::Capability().
func (c *Client) Capability() []string { return c.capabilities }

// HasCapability is SmtpClient::hasCapability().
func (c *Client) HasCapability(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, capability := range c.capabilities {
		if capability == name {
			return true
		}
	}
	return false
}

// Disconnect is SmtpClient::Logout(): QUIT and close.
func (c *Client) Disconnect() error {
	if nil == c.conn {
		return nil
	}
	_, err := c.command("QUIT", 221)
	_ = c.Close()
	return err
}

// Close releases the socket.
func (c *Client) Close() error {
	if nil == c.conn {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.reader = nil
	c.loggedIn = false
	c.mailSent = false
	c.rcptSent = false
	return err
}

// ehloOrHelo is SmtpClient::ehloOrHelo().
func (c *Client) ehloOrHelo(host string) error {
	if "" == host {
		host = "localhost"
	}
	if err := c.ehlo(host); nil == err {
		return nil
	}
	return c.helo(host)
}

// ehlo parses the multi-line EHLO capability answer.
func (c *Client) ehlo(host string) error {
	lines, err := c.commandLines("EHLO "+host, 250)
	if nil != err {
		return err
	}

	c.capabilities = nil
	c.authTypes = nil
	c.sizeValue = 0

	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		value := strings.TrimSpace(line[4:])
		if "" == value {
			continue
		}
		parts := strings.FieldsFunc(value, func(r rune) bool { return ' ' == r || '=' == r })
		if 0 == len(parts) {
			continue
		}
		capability := strings.ToUpper(parts[0])
		if 1 < len(parts) {
			switch capability {
			case "AUTH":
				c.authTypes = strings.Fields(strings.ToUpper(strings.Join(parts[1:], " ")))
			case "SIZE":
				if size, err := strconv.Atoi(parts[1]); nil == err {
					c.sizeValue = size
				}
			}
		}
		c.capabilities = append(c.capabilities, capability)
	}
	return nil
}

// helo is SmtpClient::helo().
func (c *Client) helo(host string) error {
	_, err := c.command("HELO "+host, 250)
	c.capabilities = nil
	c.authTypes = nil
	c.sizeValue = 0
	return err
}

// command sends a command and validates the expected status code.
func (c *Client) command(command string, expected int) (string, error) {
	return c.commandAny(command, []int{expected})
}

// commandAny sends a command and validates one of expected.
func (c *Client) commandAny(command string, expected []int) (string, error) {
	if err := c.writeLine(command); nil != err {
		return "", err
	}
	return c.readResponse(expected...)
}

// commandLines is commandAny for a multi-line answer, returning the raw lines.
func (c *Client) commandLines(command string, expected ...int) ([]string, error) {
	if err := c.writeLine(command); nil != err {
		return nil, err
	}
	return c.readResponseLines(expected...)
}

// readResponse reads a (possibly multi-line) answer and validates the code.
func (c *Client) readResponse(expected ...int) (string, error) {
	lines, err := c.readResponseLines(expected...)
	if nil != err {
		return "", err
	}
	if 0 == len(lines) {
		return "", errors.New("smtpclient: empty response")
	}
	if 4 <= len(lines[0]) {
		return strings.TrimSpace(lines[0][4:]), nil
	}
	return "", nil
}

// readResponseLines reads lines until the last code carries a space (not '-').
func (c *Client) readResponseLines(expected ...int) ([]string, error) {
	var lines []string
	for {
		line, err := c.readLine()
		if nil != err {
			return lines, connectionError(err)
		}
		lines = append(lines, line)
		if len(line) < 4 || (' ' != line[3] && '-' != line[3]) {
			break
		}
		if '-' != line[3] {
			break
		}
	}

	if 0 == len(lines) || len(lines[0]) < 3 {
		return lines, protocolError(errors.New("smtpclient: malformed response"))
	}
	code, err := strconv.Atoi(lines[0][:3])
	if nil != err {
		return lines, protocolError(err)
	}
	for _, wanted := range expected {
		if code == wanted {
			return lines, nil
		}
	}
	return lines, protocolError(fmt.Errorf("smtpclient: unexpected response %s", strings.Join(lines, " ")))
}

// writeLine sends one CRLF terminated line under the read deadline.
func (c *Client) writeLine(line string) error {
	if nil == c.conn {
		return connectionError(errors.New("smtpclient: not connected"))
	}
	c.setDeadline()
	_, err := io.WriteString(c.conn, line+"\r\n")
	if nil != err {
		return connectionError(err)
	}
	return nil
}

// readLine reads one CRLF terminated line without the terminator.
func (c *Client) readLine() (string, error) {
	if nil == c.reader {
		return "", errors.New("smtpclient: not connected")
	}
	c.setDeadline()
	line, err := c.reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// setDeadline applies the max(5, timeout) read timeout of PHP streams.
func (c *Client) setDeadline() {
	if 0 < c.readTimeout && nil != c.conn {
		_ = c.conn.SetDeadline(time.Now().Add(c.readTimeout))
	}
}

// readTimeout is stream_set_timeout(): max(5, timeout) seconds, or 0 when no
// timeout is configured (tests).
func readTimeout(timeout int) time.Duration {
	if 0 >= timeout {
		return 0
	}
	base := time.Duration(timeout) * time.Second
	if 5*time.Second > base {
		return 5 * time.Second
	}
	return base
}

// useSSL is ConnectionSecurityType::UseSSL() for port 465.
func useSSL(port, securityType int) bool {
	result := securityType
	if domain.SecurityAutoDetect == securityType && 465 == port {
		result = domain.SecuritySSL
	}
	return domain.SecuritySSL == result
}

// tlsConfiguration maps MailSo's SSLContext onto crypto/tls.
func tlsConfiguration(settings *domain.SSL, host string) (*tls.Config, error) {
	config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if nil == settings {
		return config, nil
	}
	if !settings.VerifyPeer || settings.AllowSelfSigned {
		config.InsecureSkipVerify = true
	}
	if "" != strings.TrimSpace(settings.CAFile) {
		content, err := os.ReadFile(settings.CAFile)
		if nil != err {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(content)
		config.RootCAs = pool
	}
	if "" != strings.TrimSpace(settings.LocalCert) {
		certificate, err := tls.LoadX509KeyPair(settings.LocalCert, settings.LocalCert)
		if nil != err {
			return nil, err
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

// normalizeCRLF converts lone LF/CR to CRLF (LineEndings stream filter).
func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

// The error constructors.

func connectionError(err error) error { return &Error{Kind: KindConnection, Err: err} }

func loginError(err error) error { return &Error{Kind: KindLogin, Err: err} }

func protocolError(err error) error { return &Error{Kind: KindOther, Err: err} }
