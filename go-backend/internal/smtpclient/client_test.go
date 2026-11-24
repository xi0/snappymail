package smtpclient

// Tests of Step 19's SMTP transport: EHLO capability parsing, AUTH PLAIN, the
// MAIL FROM/RCPT/DATA sequence, dot stuffing and the usePhpMail failure.

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// mockSMTP is a minimal ESMTP server for the sender tests.
type mockSMTP struct {
	listener net.Listener
	port     int

	mu       sync.Mutex
	commands []string
	rcpts    []string
	data     string
}

// newMockSMTP starts the server.
func newMockSMTP(t *testing.T) *mockSMTP {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}
	server := &mockSMTP{listener: listener, port: listener.Addr().(*net.TCPAddr).Port}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *mockSMTP) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *mockSMTP) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }

	write("220 mock ESMTP")

	authPending := false
	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		s.mu.Lock()
		s.commands = append(s.commands, line)
		s.mu.Unlock()

		if authPending {
			authPending = false
			write("235 authenticated")
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-mock")
			write("250-AUTH PLAIN LOGIN CRAM-MD5")
			write("250-SIZE 1000000")
			write("250 8BITMIME")

		case "AUTH PLAIN" == upper:
			authPending = true
			write("334 ")
		case strings.HasPrefix(upper, "AUTH PLAIN "):
			write("235 authenticated")

		case strings.HasPrefix(upper, "MAIL FROM:"):
			write("250 ok")

		case strings.HasPrefix(upper, "RCPT TO:"):
			start := strings.Index(line, "<")
			end := strings.Index(line, ">")
			if 0 <= start && end > start {
				s.mu.Lock()
				s.rcpts = append(s.rcpts, line[start+1:end])
				s.mu.Unlock()
			}
			write("250 ok")

		case "DATA" == upper:
			write("354 end with .")
			var builder strings.Builder
			for {
				dataLine, readErr := reader.ReadString('\n')
				if nil != readErr {
					return
				}
				if ".\r\n" == dataLine || ".\n" == dataLine {
					break
				}
				if strings.HasPrefix(dataLine, "..") {
					dataLine = dataLine[1:]
				}
				builder.WriteString(dataLine)
			}
			s.mu.Lock()
			s.data = builder.String()
			s.mu.Unlock()
			write("250 queued")

		case "QUIT" == upper:
			write("221 bye")
			return

		default:
			write("250 ok")
		}
	}
}

// accountFor builds an account whose SMTP points at the mock.
func accountFor(server *mockSMTP, usePhpMail bool) *account.Account {
	config := domain.New("example.com", domain.Defaults{})
	config.SMTP.Host = "127.0.0.1"
	config.SMTP.Port = server.port
	config.SMTP.Type = domain.SecurityNone
	config.SMTP.Timeout = 5
	config.SMTP.SSL.VerifyPeer = false
	config.SMTP.UseAuth = true
	config.SMTP.UsePhpMail = usePhpMail
	config.SMTP.SASL = []string{"PLAIN"}

	main := account.NewMainAccount()
	main.SetCredentials(config, "alice@example.com", "alice@example.com", "imap-secret",
		"alice@example.com", "smtp-secret")
	return &main.Account
}

// TestSenderSend checks the SMTP dialogue and the dot stuffing.
func TestSenderSend(t *testing.T) {
	server := newMockSMTP(t)
	sender := NewSender(nil, nil, false)

	message := "From: alice@example.com\r\n" +
		"Subject: Test\r\n\r\n" +
		".leading dot\r\nsecond line\r\n"
	if err := sender.Send(
		accountFor(server, false),
		"alice@example.com",
		[]string{"bob@example.com"},
		[]string{"hidden@example.com", "invalid recipient"},
		[]byte(message),
		false, false,
	); nil != err {
		t.Fatalf("Send: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()

	for _, want := range []string{"EHLO", "AUTH PLAIN", "MAIL FROM:<alice@example.com>", "RCPT TO:<bob@example.com>", "RCPT TO:<hidden@example.com>", "DATA", "QUIT"} {
		found := false
		for _, command := range server.commands {
			if strings.Contains(strings.ToUpper(command), strings.ToUpper(want)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q was not sent: %v", want, server.commands)
		}
	}

	for _, rcpt := range server.rcpts {
		if "invalid recipient" == rcpt {
			t.Errorf("an invalid hidden recipient must be skipped: %v", server.rcpts)
		}
	}

	if !strings.Contains(server.data, ".leading dot") || strings.Contains(server.data, "..leading dot") {
		t.Errorf("dot stuffing failed: %q", server.data)
	}
	if !strings.Contains(server.data, "second line") {
		t.Errorf("message body missing: %q", server.data)
	}
}

// TestSenderUsePhpMail checks the documented unsupported branch.
func TestSenderUsePhpMail(t *testing.T) {
	server := newMockSMTP(t)
	err := NewSender(nil, nil, false).Send(
		accountFor(server, true),
		"alice@example.com", []string{"bob@example.com"}, nil, []byte("body"), false, false,
	)
	var smtpErr *Error
	if !asSMTPError(err, &smtpErr) || KindUnsupported != smtpErr.Kind {
		t.Fatalf("err = %v, want KindUnsupported", err)
	}
}

// asSMTPError is errors.As for the local Error type.
func asSMTPError(err error, target **Error) bool {
	if nil == err {
		return false
	}
	typed, ok := err.(*Error)
	if ok {
		*target = typed
	}
	return ok
}
