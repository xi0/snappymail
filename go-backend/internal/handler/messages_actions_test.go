package handler

// Tests of Step 18: the message state mutations (seen/flagged/deleted/keyword,
// delete, move, copy) of \RainLoop\Actions\Messages, driven end to end through
// the mail layer against an in-process IMAP server.
//
// The server answers SELECT/STATUS/STORE/EXPUNGE/COPY/MOVE with a plain OK, so
// the assertions are made on the command log: the flag atoms, the sequence
// sets, the UID/sequence-number distinction, the spam learning pair and the
// MOVE-vs-COPY fallback are the observable contract of these actions.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mail"
)

// actionServer is a minimal IMAP server for the message mutations.
type actionServer struct {
	listener     net.Listener
	port         int
	capabilities string

	mu       sync.Mutex
	received []string
}

// newActionServer starts a server that advertises MOVE and UIDPLUS.
func newActionServer(t *testing.T) *actionServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &actionServer{
		listener:     listener,
		port:         listener.Addr().(*net.TCPAddr).Port,
		capabilities: "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS MOVE UNSELECT SORT PREVIEW",
	}

	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })

	return server
}

// connect opens and authenticates a mail client on the mock.
func (s *actionServer) connect(t *testing.T) *mail.Client {
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

// commands returns a copy of every received client line.
func (s *actionServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

// hasCommand reports whether a received line contains needle (case
// insensitive).
func (s *actionServer) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range s.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

// since returns the commands received after the first count lines.
func (s *actionServer) since(count int) []string {
	commands := s.commands()
	if count >= len(commands) {
		return nil
	}
	return commands[count:]
}

func (s *actionServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *actionServer) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }

	write("* OK [CAPABILITY " + s.capabilities + "] mock ready")

	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		s.mu.Lock()
		s.received = append(s.received, line)
		s.mu.Unlock()

		tag, rest := splitArg(line, " ")
		command, _ := splitArg(rest, " ")
		command = strings.ToUpper(command)

		// Every step 18 command may be UID prefixed; the raw line is recorded,
		// the dispatch is done on the command name.
		if "UID" == command {
			rest = strings.TrimLeft(rest[len(command):], " ")
			command, _ = splitArg(rest, " ")
			command = strings.ToUpper(command)
		}

		quoted := quotedStrings(rest)
		folder := ""
		if 0 < len(quoted) {
			folder = quoted[0]
		}

		switch command {
		case "AUTHENTICATE", "LOGIN":
			write(tag + " OK authenticated")

		case "CAPABILITY":
			write("* CAPABILITY " + s.capabilities)
			write(tag + " OK CAPABILITY completed")

		case "UNSELECT":
			write(tag + " OK UNSELECT completed")

		case "SELECT", "EXAMINE":
			// A mailbox with `\*` advertises "any flag"; the others only the
			// five system flags.
			permanent := `\Seen \Deleted \Flagged \Answered \Draft`
			if "Starred" == folder {
				permanent = `\Seen \*`
			}
			write("* 2 EXISTS")
			write("* OK [UIDVALIDITY 1] UIDs valid")
			write("* OK [UIDNEXT 3] Predicted next UID")
			write("* OK [PERMANENTFLAGS (" + permanent + ")] Limited")
			write(tag + " OK [READ-WRITE] " + command + " completed")

		case "STATUS":
			if "NoHash" == folder {
				write(tag + " NO no such mailbox")
				continue
			}
			name := folder
			if "" == name {
				name = "INBOX"
			}
			write(fmt.Sprintf(`* STATUS "%s" (MESSAGES 2 UNSEEN 1 UIDNEXT 3 UIDVALIDITY 1)`, name))
			write(tag + " OK STATUS completed")

		case "STORE", "EXPUNGE", "COPY", "MOVE", "NOOP":
			write(tag + " OK " + command + " completed")

		case "LOGOUT":
			write("* BYE logging out")
			write(tag + " OK LOGOUT completed")
			return

		default:
			write(tag + " BAD unknown command")
		}
	}
}

// splitArg splits value on the first occurrence of separator.
func splitArg(value, separator string) (string, string) {
	if index := strings.Index(value, separator); 0 <= index {
		return value[:index], value[index+len(separator):]
	}
	return value, ""
}

// quotedStrings returns the quoted strings of a command line.
func quotedStrings(line string) []string {
	var result []string
	for index := 0; index < len(line); index++ {
		if '"' != line[index] {
			continue
		}
		var builder strings.Builder
		index++
		for ; index < len(line) && '"' != line[index]; index++ {
			if '\\' == line[index] && index+1 < len(line) {
				index++
			}
			builder.WriteByte(line[index])
		}
		result = append(result, builder.String())
	}
	return result
}

// actionContext builds the request context of one mutation on the mock.
func actionContext(t *testing.T, server *actionServer, params map[string]any) *Context {
	t.Helper()

	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := mailDispatcher(session, fakeMail{resolved: resolved, client: server.connect(t)})

	return folderContext(dispatcher, session, params)
}

// resultOf runs an action and unwraps the "Result" of its envelope.
func resultOf(t *testing.T, action ActionFunc, ctx *Context) any {
	t.Helper()

	result, err := action(ctx)
	if nil != err {
		t.Fatalf("action failed: %v", err)
	}
	response, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("response = %T", result)
	}
	return response["Result"]
}

// TestMessageActionsAreRegistered checks the registry of the step 18 actions.
func TestMessageActionsAreRegistered(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	for _, action := range []string{
		"MessageSetSeen", "MessageSetSeenToAll", "MessageSetFlagged",
		"MessageSetDeleted", "MessageSetKeyword", "MessageDelete",
		"MessageMove", "MessageCopy",
	} {
		if !dispatcher.registry.Has(action) {
			t.Errorf("%s is not registered", action)
		}
	}
}

// TestMessageActionsNeedAnAccount checks the InvalidToken failure (101) of a
// request without a session, and that a connection failure is propagated.
func TestMessageActionsNeedAnAccount(t *testing.T) {
	noMail := NewDispatcher(Options{Version: "0.0.0"})

	for _, test := range []struct {
		name   string
		action ActionFunc
	}{
		{"MessageSetSeen", doMessageSetSeen},
		{"MessageSetSeenToAll", doMessageSetSeenToAll},
		{"MessageSetFlagged", doMessageSetFlagged},
		{"MessageSetDeleted", doMessageSetDeleted},
		{"MessageSetKeyword", doMessageSetKeyword},
		{"MessageDelete", doMessageDelete},
		{"MessageMove", doMessageMove},
		{"MessageCopy", doMessageCopy},
	} {
		_, err := test.action(folderContext(noMail, nil, map[string]any{}))
		if appctx.InvalidToken != clientCodeOf(err) {
			t.Errorf("%s guest code = %d, want %d", test.name, clientCodeOf(err), appctx.InvalidToken)
		}
	}

	// A failing initMailClientConnection() is reported unchanged.
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := mailDispatcher(session, fakeMail{
		resolved: resolved,
		err:      appctx.NewClientError(appctx.AuthError, nil, ""),
	})
	if _, err := doMessageSetSeen(folderContext(dispatcher, session, map[string]any{})); appctx.AuthError != clientCodeOf(err) {
		t.Errorf("connection error code = %d, want %d", clientCodeOf(err), appctx.AuthError)
	}
}

// TestMessageSetSeen checks the +FLAGS.SILENT/-FLAGS.SILENT pair of
// DoMessageSetSeen and the UID sequence-set rendering.
func TestMessageSetSeen(t *testing.T) {
	server := newActionServer(t)

	result := resultOf(t, doMessageSetSeen, actionContext(t, server, map[string]any{
		"folder":    "INBOX",
		"uids":      "3,1,2",
		"setAction": "1",
	}))
	if true != result {
		t.Errorf("Result = %v, want true", result)
	}
	// The set is validated and compressed: "3,1,2" -> "3,1:2". The flag atom is
	// sent as an IMAP quoted string (see mail/flags.go).
	if !server.hasCommand(`UID STORE 3,1:2 +FLAGS.SILENT ("\\Seen")`) {
		t.Errorf("STORE +FLAGS.SILENT \\Seen was not sent: %v", server.commands())
	}

	// setAction 0 removes the flag again.
	resultOf(t, doMessageSetSeen, actionContext(t, server, map[string]any{
		"folder":    "INBOX",
		"uids":      "5",
		"setAction": "0",
	}))
	if !server.hasCommand(`UID STORE 5 -FLAGS.SILENT ("\\Seen")`) {
		t.Errorf("STORE -FLAGS.SILENT \\Seen was not sent: %v", server.commands())
	}

	// An empty uid list is a no-op that still reports success.
	mark := len(server.commands())
	resultOf(t, doMessageSetSeen, actionContext(t, server, map[string]any{
		"folder":    "INBOX",
		"uids":      "",
		"setAction": "1",
	}))
	for _, line := range server.since(mark) {
		if strings.Contains(strings.ToUpper(line), "STORE") {
			t.Errorf("an empty uid list must not STORE: %q", line)
		}
	}
}

// TestMessageSetFlaggedAndDeleted checks the two skipUnsupportedFlag actions.
func TestMessageSetFlaggedAndDeleted(t *testing.T) {
	server := newActionServer(t)

	resultOf(t, doMessageSetFlagged, actionContext(t, server, map[string]any{
		"folder": "INBOX", "uids": "2", "setAction": "1",
	}))
	if !server.hasCommand(`UID STORE 2 +FLAGS.SILENT ("\\Flagged")`) {
		t.Errorf("\\Flagged was not stored: %v", server.commands())
	}

	resultOf(t, doMessageSetDeleted, actionContext(t, server, map[string]any{
		"folder": "INBOX", "uids": "2", "setAction": "1",
	}))
	if !server.hasCommand(`UID STORE 2 +FLAGS.SILENT ("\\Deleted")`) {
		t.Errorf("\\Deleted was not stored: %v", server.commands())
	}
}

// TestMessageSetKeywordUnsupported checks that a keyword the mailbox does not
// advertise as permanent is silently accepted (skipUnsupportedFlag=true), and
// stored when the mailbox advertises `\*`.
func TestMessageSetKeywordUnsupported(t *testing.T) {
	server := newActionServer(t)

	// INBOX advertises only the five system flags: $NotJunk is unsupported, so
	// no STORE is sent, but the action still succeeds.
	result := resultOf(t, doMessageSetKeyword, actionContext(t, server, map[string]any{
		"folder": "INBOX", "uids": "1", "keyword": "$NotJunk", "setAction": "1",
	}))
	if true != result {
		t.Errorf("Result = %v, want true", result)
	}
	if server.hasCommand("$NotJunk") {
		t.Errorf("an unsupported keyword must not be stored: %v", server.commands())
	}

	// A mailbox with `\*` supports any keyword.
	resultOf(t, doMessageSetKeyword, actionContext(t, server, map[string]any{
		"folder": "Starred", "uids": "1", "keyword": "$NotJunk", "setAction": "1",
	}))
	if !server.hasCommand(`UID STORE 1 +FLAGS.SILENT ("$NotJunk")`) {
		t.Errorf("the keyword was not stored: %v", server.commands())
	}
}

// TestMessageSetSeenToAll checks the two forms of DoMessageSetSeenToAll: the
// whole mailbox by sequence number ("1:*") and the thread uids.
func TestMessageSetSeenToAll(t *testing.T) {
	server := newActionServer(t)

	resultOf(t, doMessageSetSeenToAll, actionContext(t, server, map[string]any{
		"folder": "INBOX", "setAction": "1", "threadUids": "",
	}))
	if !server.hasCommand(`STORE 1:* +FLAGS.SILENT ("\\Seen")`) {
		t.Errorf(`STORE 1:* was not sent by sequence number: %v`, server.commands())
	}

	resultOf(t, doMessageSetSeenToAll, actionContext(t, server, map[string]any{
		"folder": "INBOX", "setAction": "1", "threadUids": "7,8",
	}))
	if !server.hasCommand(`UID STORE 7:8 +FLAGS.SILENT ("\\Seen")`) {
		t.Errorf("the thread uids were not stored as UIDs: %v", server.commands())
	}
}

// TestMessageDelete checks the [folder, hash] result and the ported PHP quirk of
// the folder without a STATUS hash.
func TestMessageDelete(t *testing.T) {
	server := newActionServer(t)

	result, _ := resultOf(t, doMessageDelete, actionContext(t, server, map[string]any{
		"folder": "INBOX", "uids": "4,5",
	})).([]any)
	if nil == result || 2 != len(result) {
		t.Fatalf("Result = %v, want a [folder, hash] pair", result)
	}
	if "INBOX" != result[0] {
		t.Errorf("folder = %v", result[0])
	}
	if hash, _ := result[1].(string); "" == hash {
		t.Errorf("hash = %v, want the folder etag", result[1])
	}
	if !server.hasCommand("UID EXPUNGE 4:5") {
		t.Errorf("UID EXPUNGE was not sent: %v", server.commands())
	}

	// PHP returns array($sFromFolder) there, and $sFromFolder is never set in
	// DoMessageDelete, so the fallback is [null].
	result, _ = resultOf(t, doMessageDelete, actionContext(t, server, map[string]any{
		"folder": "NoHash", "uids": "1",
	})).([]any)
	if nil == result || 1 != len(result) || nil != result[0] {
		t.Errorf("Result without a hash = %v, want [null]", result)
	}
}

// TestMessageMove checks markAsRead, the spam learning pair and the MOVE
// command (the server advertises MOVE).
//
// The source mailbox is "Starred", the only one of the mock that advertises
// `\*` in [PERMANENTFLAGS]: $Junk/$NotJunk are not permanent in "INBOX", so
// MessageSetFlag() would refuse them there (exactly like PHP, where the
// failures of the learning stores are caught and ignored).
func TestMessageMove(t *testing.T) {
	server := newActionServer(t)

	result, _ := resultOf(t, doMessageMove, actionContext(t, server, map[string]any{
		"fromFolder": "Starred",
		"toFolder":   "Junk",
		"uids":       "1,2,3",
		"markAsRead": "1",
		"learning":   "SPAM",
	})).([]any)
	if nil == result || 2 != len(result) || "Starred" != result[0] {
		t.Fatalf("Result = %v, want [Starred, hash]", result)
	}

	for _, want := range []string{
		`UID STORE 1:3 +FLAGS.SILENT ("\\Seen")`,
		`UID STORE 1:3 +FLAGS.SILENT ("$Junk")`,
		`UID STORE 1:3 -FLAGS.SILENT ("$NotJunk")`,
		`UID MOVE 1:3 "Junk"`,
	} {
		if !server.hasCommand(want) {
			t.Errorf("%q was not sent: %v", want, server.commands())
		}
	}

	// "HAM" learns the other way round, and a request without markAsRead does
	// not touch the \Seen flag.
	server = newActionServer(t)
	resultOf(t, doMessageMove, actionContext(t, server, map[string]any{
		"fromFolder": "Starred",
		"toFolder":   "INBOX",
		"uids":       "9",
		"learning":   "HAM",
	}))
	for _, want := range []string{
		`UID STORE 9 +FLAGS.SILENT ("$NotJunk")`,
		`UID STORE 9 -FLAGS.SILENT ("$Junk")`,
	} {
		if !server.hasCommand(want) {
			t.Errorf("%q was not sent: %v", want, server.commands())
		}
	}
	if server.hasCommand(`+FLAGS.SILENT ("\\Seen")`) {
		t.Errorf("markAsRead was not requested: %v", server.commands())
	}
}

// TestMessageCopy checks the [toFolder, hash] result of DoMessageCopy.
func TestMessageCopy(t *testing.T) {
	server := newActionServer(t)

	result, _ := resultOf(t, doMessageCopy, actionContext(t, server, map[string]any{
		"fromFolder": "INBOX",
		"toFolder":   "Archive",
		"uids":       "1,2",
	})).([]any)
	if nil == result || 2 != len(result) {
		t.Fatalf("Result = %v, want a [folder, hash] pair", result)
	}
	if "Archive" != result[0] {
		t.Errorf("folder = %v, want the destination", result[0])
	}
	if hash, _ := result[1].(string); "" == hash {
		t.Errorf("hash = %v, want the destination etag", result[1])
	}
	if !server.hasCommand(`UID COPY 1:2 "Archive"`) {
		t.Errorf("UID COPY was not sent: %v", server.commands())
	}
}

// TestMessageMoveCopyFailures checks the CantCopyMessage (205) and
// CantMoveMessage (204) codes of the refused commands.
func TestMessageMoveCopyFailures(t *testing.T) {
	server := newActionServer(t)

	// An empty destination is refused by the command layer before the COPY.
	copyCtx := actionContext(t, server, map[string]any{
		"fromFolder": "INBOX", "toFolder": "", "uids": "1",
	})
	if _, err := doMessageCopy(copyCtx); appctx.CantCopyMessage != clientCodeOf(err) {
		t.Errorf("copy code = %d, want %d", clientCodeOf(err), appctx.CantCopyMessage)
	}

	// An empty uid list leaves nothing to move.
	moveCtx := actionContext(t, server, map[string]any{
		"fromFolder": "INBOX", "toFolder": "Archive", "uids": "",
	})
	if _, err := doMessageMove(moveCtx); appctx.CantMoveMessage != clientCodeOf(err) {
		t.Errorf("move code = %d, want %d", clientCodeOf(err), appctx.CantMoveMessage)
	}
}
