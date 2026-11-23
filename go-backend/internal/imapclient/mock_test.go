package imapclient

// Test helpers: a tiny in-process IMAP server that speaks just enough of the
// protocol (greeting, CAPABILITY, STARTTLS, AUTHENTICATE PLAIN, LOGIN, ENABLE,
// LOGOUT) to exercise the connect/capability/login paths against a real socket.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// mockIMAP is a minimal IMAP server for the tests.
type mockIMAP struct {
	listener net.Listener
	port     int

	greeting     string
	capabilities string
	tlsConfig    *tls.Config
	user         string
	pass         string

	mu          sync.Mutex
	received    []string
	tlsUpgraded bool
}

// newMockIMAP starts a server with a greeting that advertises PLAIN + SASL-IR
// and the credentials user@example.com / secret.
func newMockIMAP(t *testing.T) *mockIMAP {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &mockIMAP{
		listener:     listener,
		port:         listener.Addr().(*net.TCPAddr).Port,
		greeting:     "* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN SASL-IR] mock ready",
		capabilities: "IMAP4rev1 AUTH=PLAIN SASL-IR",
		user:         "user@example.com",
		pass:         "secret",
	}

	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })

	return server
}

// settings builds a plain (no TLS) IMAP settings value pointing at the mock.
func (m *mockIMAP) settings() *domain.IMAPSettings {
	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = m.port
	settings.Type = domain.SecurityNone
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false
	return &settings
}

// domainConfig builds a domain config pointing at the mock, for ConnectAndLogin.
func (m *mockIMAP) domainConfig() *domain.Domain {
	config := domain.New("example.com", domain.Defaults{})
	config.IMAP.Host = "127.0.0.1"
	config.IMAP.Port = m.port
	config.IMAP.Type = domain.SecurityNone
	config.IMAP.Timeout = 5
	config.IMAP.SSL.VerifyPeer = false
	return config
}

func (m *mockIMAP) acceptLoop() {
	for {
		connection, err := m.listener.Accept()
		if nil != err {
			return
		}
		go m.handle(connection)
	}
}

func (m *mockIMAP) record(line string) {
	m.mu.Lock()
	m.received = append(m.received, line)
	m.mu.Unlock()
}

// commands returns a copy of every client line the server received.
func (m *mockIMAP) commands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.received...)
}

// hasCommand reports whether any received line contains needle (upper case
// comparison).
func (m *mockIMAP) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range m.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

func (m *mockIMAP) wasTLSUpgraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tlsUpgraded
}

func (m *mockIMAP) handle(connection net.Conn) {
	defer connection.Close()

	var current net.Conn = connection
	reader := bufio.NewReader(current)
	writeLine := func(line string) {
		_, _ = current.Write([]byte(line + "\r\n"))
	}

	writeLine(m.greeting)

	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		m.record(line)

		tag, commandLine := splitFirst(line, " ")
		command, rest := splitFirst(commandLine, " ")

		switch strings.ToUpper(command) {
		case "CAPABILITY":
			writeLine("* CAPABILITY " + m.capabilities)
			writeLine(tag + " OK CAPABILITY completed")

		case "STARTTLS":
			m.mu.Lock()
			m.tlsUpgraded = true
			m.mu.Unlock()
			writeLine(tag + " OK begin TLS")
			secured := tls.Server(current, m.tlsConfig)
			if err := secured.Handshake(); nil != err {
				return
			}
			current = secured
			reader = bufio.NewReader(current)

		case "AUTHENTICATE":
			m.handleAuthenticate(tag, rest, reader, writeLine)

		case "LOGIN":
			args := parseQuoted(rest)
			if 2 <= len(args) && args[0] == m.user && args[1] == m.pass {
				writeLine(tag + " OK logged in")
			} else {
				writeLine(tag + " NO [AUTHENTICATIONFAILED] Invalid credentials")
			}

		case "ENABLE":
			writeLine("* ENABLED " + strings.ToUpper(rest))
			writeLine(tag + " OK ENABLE completed")

		case "LOGOUT":
			writeLine("* BYE logging out")
			writeLine(tag + " OK LOGOUT completed")
			return

		case "NOOP":
			writeLine(tag + " OK NOOP completed")

		default:
			writeLine(tag + " BAD unknown command")
		}
	}
}

func (m *mockIMAP) handleAuthenticate(tag, rest string, reader *bufio.Reader, writeLine func(string)) {
	mechanism, initial := splitFirst(rest, " ")

	switch strings.ToUpper(mechanism) {
	case "PLAIN":
		payload := initial
		if "" == payload {
			writeLine("+ ")
			line, err := reader.ReadString('\n')
			if nil != err {
				return
			}
			payload = strings.TrimRight(line, "\r\n")
			m.record(payload)
		}

		fields := strings.Split(string(mustB64(payload)), "\x00")
		if 3 <= len(fields) && fields[1] == m.user && fields[2] == m.pass {
			writeLine(tag + " OK authenticated")
		} else {
			writeLine(tag + " NO [AUTHENTICATIONFAILED] Invalid credentials")
		}

	default:
		writeLine(tag + " NO unsupported mechanism")
	}
}

// autoTLSConfig builds a self signed certificate for the STARTTLS test server.
func autoTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if nil != err {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if nil != err {
		t.Fatalf("create certificate: %v", err)
	}

	return &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}}}
}

// mustB64 decodes standard base64, tolerating missing padding.
func mustB64(value string) []byte {
	if data, err := base64.StdEncoding.DecodeString(value); nil == err {
		return data
	}
	data, _ := base64.RawStdEncoding.DecodeString(value)
	return data
}

// splitFirst splits value on the first occurrence of separator.
func splitFirst(value, separator string) (string, string) {
	if index := strings.Index(value, separator); 0 <= index {
		return value[:index], value[index+len(separator):]
	}
	return value, ""
}

// parseQuoted returns the space separated (optionally quoted) arguments of an
// IMAP command line.
func parseQuoted(value string) []string {
	var result []string
	index := 0
	for index < len(value) {
		for index < len(value) && ' ' == value[index] {
			index++
		}
		if index >= len(value) {
			break
		}
		if '"' == value[index] {
			index++
			var builder strings.Builder
			for index < len(value) && '"' != value[index] {
				if '\\' == value[index] && index+1 < len(value) {
					index++
				}
				builder.WriteByte(value[index])
				index++
			}
			if index < len(value) && '"' == value[index] {
				index++
			}
			result = append(result, builder.String())
			continue
		}
		start := index
		for index < len(value) && ' ' != value[index] {
			index++
		}
		result = append(result, value[start:index])
	}
	return result
}
