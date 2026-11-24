package handler

// Tests of Step 19: DoSaveMessage/DoSendMessage against an in-process IMAP
// server and a fake SMTP sender. The assertions cover the draft APPEND flags,
// the [APPENDUID]/Message-ID UID resolution, the Bcc handling of the Sent copy
// and the notification codes of the send failures.

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/smtpclient"
)

// sendServer is a minimal IMAP server for the save/send actions.
type sendServer struct {
	listener    net.Listener
	port        int
	noAppendUID bool

	mu        sync.Mutex
	commands  []string
	appended  [][]byte
	appendCmd []string
}

// newSendServer starts the mock and serves until the test ends.
func newSendServer(t *testing.T, noAppendUID bool) *sendServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &sendServer{
		listener:    listener,
		port:        listener.Addr().(*net.TCPAddr).Port,
		noAppendUID: noAppendUID,
	}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

// connect opens and authenticates a mail client on the mock.
func (s *sendServer) connect(t *testing.T) *mail.Client {
	t.Helper()

	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = s.port
	settings.Type = domain.SecurityNone
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false

	client := imapclient.New(&settings, nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mail.New(client)
}

// hasCommand reports a received command containing needle (case insensitive).
func (s *sendServer) hasCommand(needle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	needle = strings.ToUpper(needle)
	for _, line := range s.commands {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

// stream returns the n-th appended message.
func (s *sendServer) stream(index int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.appended) {
		return ""
	}
	return string(s.appended[index])
}

// count returns the number of appended messages.
func (s *sendServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appended)
}

func (s *sendServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *sendServer) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }

	write("* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS UNSELECT] mock ready")

	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		s.mu.Lock()
		s.commands = append(s.commands, line)
		s.mu.Unlock()

		tag, rest := splitArg(line, " ")
		command, args := splitArg(rest, " ")
		command = strings.ToUpper(command)
		uid := false
		if "UID" == command {
			uid = true
			command, args = splitArg(args, " ")
			command = strings.ToUpper(command)
		}

		switch command {
		case "AUTHENTICATE", "LOGIN":
			write(tag + " OK authenticated")

		case "CAPABILITY":
			write("* CAPABILITY IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS UNSELECT")
			write(tag + " OK CAPABILITY completed")

		case "EXAMINE", "SELECT":
			write("* 2 EXISTS")
			write("* OK [UIDVALIDITY 1] UIDs valid")
			write("* OK [UIDNEXT 100] Predicted next UID")
			write("* OK [PERMANENTFLAGS (\\Seen \\Deleted)] Limited")
			write(tag + " OK [READ-WRITE] " + command + " completed")

		case "UNSELECT", "STORE", "EXPUNGE", "COPY", "MOVE", "NOOP":
			write(tag + " OK " + command + " completed")

		case "SEARCH":
			write("* SEARCH 77")
			write(tag + " OK SEARCH completed")

		case "APPEND":
			s.handleAppend(tag, args, reader, write)

		case "LOGOUT":
			write("* BYE logging out")
			write(tag + " OK LOGOUT completed")
			return

		default:
			_ = uid
			write(tag + " BAD unknown command")
		}
	}
}

// handleAppend reads the literal of an APPEND and answers with APPENDUID.
func (s *sendServer) handleAppend(tag, args string, reader *bufio.Reader, write func(string)) {
	open := strings.LastIndex(args, "{")
	closeAt := strings.LastIndex(args, "}")
	if 0 > open || closeAt <= open {
		write(tag + " BAD APPEND syntax")
		return
	}
	size, err := strconv.Atoi(args[open+1 : closeAt])
	if nil != err {
		write(tag + " BAD APPEND size")
		return
	}

	s.mu.Lock()
	s.appendCmd = append(s.appendCmd, args)
	s.mu.Unlock()

	write("+ Ready for literal data")
	body := make([]byte, size)
	if _, err := io.ReadFull(reader, body); nil != err {
		return
	}
	_, _ = reader.ReadString('\n')

	s.mu.Lock()
	s.appended = append(s.appended, body)
	s.mu.Unlock()

	if s.noAppendUID {
		write(tag + " OK APPEND completed")
		return
	}
	write(tag + " OK [APPENDUID 1 42] APPEND completed")
}

// fakeSender is the handler.Sender of the send tests.
type fakeSender struct {
	calls      int
	from       string
	recipients []string
	hidden     []string
	data       []byte
	err        error
}

// Send records the invocation and returns the configured error.
func (f *fakeSender) Send(target *account.Account, from string, recipients, hidden []string, data []byte, dsn, requireTLS bool) error {
	f.calls++
	f.from = from
	f.recipients = append([]string(nil), recipients...)
	f.hidden = append([]string(nil), hidden...)
	f.data = append([]byte(nil), data...)
	return f.err
}

// sendDispatcher builds a dispatcher with the mock IMAP client and fake sender.
func sendDispatcher(session Session, client *mail.Client, sender Sender) *Dispatcher {
	return NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Mail: func(http.ResponseWriter, *http.Request) Mail {
			return fakeMail{resolved: session.(*folderSession).account, client: client}
		},
		SMTP: func(Plugins) Sender { return sender },
	})
}

// sendContext builds the context of a save/send action.
func sendContext(t *testing.T, server *sendServer, sender Sender, params map[string]any) *Context {
	t.Helper()
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := sendDispatcher(session, server.connect(t), sender)
	return folderContext(dispatcher, session, params)
}

// TestMessageSendActionsAreRegistered checks the registry of step 19.
func TestMessageSendActionsAreRegistered(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})
	for _, action := range []string{"SaveMessage", "SendMessage", "SendReadReceiptMessage"} {
		if !dispatcher.registry.Has(action) {
			t.Errorf("%s is not registered", action)
		}
	}
}

// TestSaveMessageNeedsAnAccount checks the InvalidToken failure (101) and the
// UnknownError of a missing save folder.
func TestSaveMessageNeedsAnAccount(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})
	if _, err := doSaveMessage(folderContext(dispatcher, nil, map[string]any{})); appctx.InvalidToken != clientCodeOf(err) {
		t.Errorf("guest code = %d, want %d", clientCodeOf(err), appctx.InvalidToken)
	}

	server := newSendServer(t, false)
	if _, err := doSaveMessage(sendContext(t, server, nil, map[string]any{})); appctx.UnknownError != clientCodeOf(err) {
		t.Errorf("missing saveFolder code = %d, want %d", clientCodeOf(err), appctx.UnknownError)
	}
}

// TestSaveMessageAppendsTheDraft checks the APPEND, its \Seen flag and the
// returned {folder, uid} pair from APPENDUID.
func TestSaveMessageAppendsTheDraft(t *testing.T) {
	server := newSendServer(t, false)
	ctx := sendContext(t, server, nil, map[string]any{
		"saveFolder": "Drafts",
		"from":       "Alice <alice@example.com>",
		"to":         "bob@example.com",
		"subject":    "Draft",
		"plain":      "Draft body",
	})

	result, err := doSaveMessage(ctx)
	if nil != err {
		t.Fatalf("doSaveMessage: %v", err)
	}
	response, _ := result.(map[string]any)
	pair, _ := response["Result"].(map[string]any)
	if nil == pair || "Drafts" != pair["folder"] || 42 != pair["uid"] {
		t.Fatalf("Result = %v, want {Drafts,42}", response["Result"])
	}

	if 1 != server.count() {
		t.Fatalf("appended %d messages, want 1", server.count())
	}
	document := server.stream(0)
	for _, want := range []string{
		"From: \"Alice\" <alice@example.com>",
		"To: bob@example.com",
		"Subject: Draft",
		"\r\n\r\nDraft body",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("draft is missing %q:\n%s", want, document)
		}
	}
	if !server.hasCommand(`APPEND "Drafts" ("\\Seen")`) {
		t.Errorf("APPEND without the \\Seen flag: %v", server.commands)
	}
}

// TestSaveMessageFindsTheUID checks the Message-ID fallback for a server that
// does not report APPENDUID.
func TestSaveMessageFindsTheUID(t *testing.T) {
	server := newSendServer(t, true)
	ctx := sendContext(t, server, nil, map[string]any{
		"saveFolder": "Drafts",
		"from":       "alice@example.com",
		"to":         "bob@example.com",
		"subject":    "Draft",
		"plain":      "body",
	})

	result, err := doSaveMessage(ctx)
	if nil != err {
		t.Fatalf("doSaveMessage: %v", err)
	}
	response, _ := result.(map[string]any)
	pair, _ := response["Result"].(map[string]any)
	if nil == pair || 77 != pair["uid"] {
		t.Fatalf("Result = %v, want the searched uid 77", response["Result"])
	}
	if !server.hasCommand("HEADER Message-ID") {
		t.Errorf("the Message-ID search was not sent: %v", server.commands)
	}
}

// TestSendMessageNeedsRecipients checks the InvalidRecipients failure (303).
func TestSendMessageNeedsRecipients(t *testing.T) {
	server := newSendServer(t, false)
	sender := &fakeSender{}
	ctx := sendContext(t, server, sender, map[string]any{
		"from":    "alice@example.com",
		"subject": "No recipients",
		"plain":   "body",
	})

	if _, err := doSendMessage(ctx); appctx.InvalidRecipients != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidRecipients)
	}
	if 0 != sender.calls {
		t.Errorf("sender was called %d times", sender.calls)
	}
}

// TestSendMessageSendsAndSaves checks the SMTP stream (Bcc stripped) and the
// Sent copy (Bcc preserved), plus the \Seen APPEND flag.
func TestSendMessageSendsAndSaves(t *testing.T) {
	server := newSendServer(t, false)
	sender := &fakeSender{}
	ctx := sendContext(t, server, sender, map[string]any{
		"from":       "alice@example.com",
		"to":         "bob@example.com",
		"bcc":        "secret@example.com",
		"subject":    "Hello",
		"plain":      "Message body",
		"saveFolder": "Sent",
	})

	result, err := doSendMessage(ctx)
	if nil != err {
		t.Fatalf("doSendMessage: %v", err)
	}
	response, _ := result.(map[string]any)
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}

	if 1 != sender.calls {
		t.Fatalf("sender calls = %d, want 1", sender.calls)
	}
	if strings.Contains(string(sender.data), "Bcc:") {
		t.Errorf("the SMTP stream must not carry Bcc:\n%s", sender.data)
	}
	if "alice@example.com" != sender.from {
		t.Errorf("from = %q", sender.from)
	}
	joined := strings.Join(sender.recipients, ",")
	if !strings.Contains(joined, "bob@example.com") || !strings.Contains(joined, "secret@example.com") {
		t.Errorf("recipients = %v, want To and Bcc", sender.recipients)
	}

	if 1 != server.count() {
		t.Fatalf("appended %d messages, want 1", server.count())
	}
	if !strings.Contains(server.stream(0), "Bcc: secret@example.com") {
		t.Errorf("the Sent copy must keep Bcc:\n%s", server.stream(0))
	}
	if !server.hasCommand(`APPEND "Sent" ("\\Seen")`) {
		t.Errorf("the Sent copy was not appended with \\Seen: %v", server.commands)
	}
}

// TestSendMessageMapsSMTPErrors checks the notification codes of the sender.
func TestSendMessageMapsSMTPErrors(t *testing.T) {
	tests := []struct {
		kind smtpclient.ErrorKind
		want int
	}{
		{smtpclient.KindLogin, appctx.AuthError},
		{smtpclient.KindConnection, appctx.ConnectionError},
		{smtpclient.KindSize, appctx.ClientViewError},
		{smtpclient.KindUnsupported, appctx.CantSendMessage},
		{smtpclient.KindOther, appctx.CantSendMessage},
	}

	for _, test := range tests {
		server := newSendServer(t, false)
		sender := &fakeSender{err: &smtpclient.Error{Kind: test.kind, Err: io.ErrUnexpectedEOF}}
		ctx := sendContext(t, server, sender, map[string]any{
			"from":    "alice@example.com",
			"to":      "bob@example.com",
			"subject": "Failure",
			"plain":   "body",
		})

		_, err := doSendMessage(ctx)
		if test.want != clientCodeOf(err) {
			t.Errorf("kind %d code = %d, want %d", test.kind, clientCodeOf(err), test.want)
		}
		if 0 != server.count() {
			t.Error("a failed send must not save a copy")
		}
	}
}

// TestSendReadReceiptNeedsTheParams checks the UnknownError (999) of an
// incomplete read receipt request.
func TestSendReadReceiptNeedsTheParams(t *testing.T) {
	server := newSendServer(t, false)
	ctx := sendContext(t, server, &fakeSender{}, map[string]any{"readReceipt": "bob@example.com"})
	if _, err := doSendReadReceiptMessage(ctx); appctx.UnknownError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.UnknownError)
	}
}
