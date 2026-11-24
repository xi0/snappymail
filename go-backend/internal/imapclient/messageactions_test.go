package imapclient

// Tests of Step 18: the MessageCopy/MessageMove/MessageDelete commands and the
// UID EXPUNGE range of FolderExpunge, against the folder mock server of
// Step 15 (which answers the mutating commands with a plain OK; the assertions
// are made on the command log).

import (
	"strings"
	"testing"
)

// actionServer starts a server with the given capabilities and one INBOX.
func actionServer(t *testing.T, capabilities string) *folderServer {
	t.Helper()
	return newFolderServer(t, capabilities,
		"* OK [CAPABILITY "+capabilities+"] mock ready",
		&mockFolder{name: "INBOX", messages: 2, unseen: 1, uidNext: 3, uidValidity: 1},
	)
}

// plainExpunge reports whether a bare "EXPUNGE" (without the UID prefix) was
// sent, i.e. the whole mailbox was expunged.
func (s *folderServer) plainExpunge() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.received {
		upper := strings.ToUpper(line)
		if strings.HasSuffix(upper, " EXPUNGE") && !strings.Contains(upper, "UID EXPUNGE") {
			return true
		}
	}
	return false
}

// TestMessageCopy checks the COPY/UID COPY rendering and the argument guard.
func TestMessageCopy(t *testing.T) {
	server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS")
	client := server.connect(t)

	if err := client.MessageCopy("INBOX", "Archive", NewSequenceSet([]any{1, 2, 3}, true)); nil != err {
		t.Fatalf("MessageCopy: %v", err)
	}
	if !server.hasCommand(`UID COPY 1:3 "Archive"`) {
		t.Errorf("UID COPY was not sent: %v", server.commands())
	}

	// A sequence-number set uses the plain COPY, like SequenceSet->UID.
	if err := client.MessageCopy("INBOX", "Archive", NewSequenceSet([]any{4}, false)); nil != err {
		t.Fatalf("MessageCopy (sequence numbers): %v", err)
	}
	if !server.hasCommand(`COPY 4 "Archive"`) {
		t.Errorf("COPY was not sent: %v", server.commands())
	}

	// An empty folder or an empty/invalid range is refused before the command.
	for _, test := range []struct {
		from, to string
		rangeSet *SequenceSet
	}{
		{"", "Archive", NewSequenceSet([]any{1}, true)},
		{"INBOX", "", NewSequenceSet([]any{1}, true)},
		{"INBOX", "Archive", NewSequenceSet([]any{}, true)},
		{"INBOX", "Archive", nil},
	} {
		if err := client.MessageCopy(test.from, test.to, test.rangeSet); nil == err {
			t.Errorf("MessageCopy(%q, %q, %v) must fail", test.from, test.to, test.rangeSet)
		}
	}
}

// TestMessageMoveUsesMoveCapability checks the RFC 6851 MOVE path.
func TestMessageMoveUsesMoveCapability(t *testing.T) {
	server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS MOVE")
	client := server.connect(t)

	if err := client.MessageMove("INBOX", "Trash", NewSequenceSet([]any{1, 2, 3}, true)); nil != err {
		t.Fatalf("MessageMove: %v", err)
	}
	if !server.hasCommand(`UID MOVE 1:3 "Trash"`) {
		t.Errorf("UID MOVE was not sent: %v", server.commands())
	}
	if server.hasCommand("COPY") {
		t.Errorf("MOVE must not COPY: %v", server.commands())
	}
}

// TestMessageMoveFallsBackToCopyAndDelete checks the copy+delete fallback of a
// server without MOVE.
func TestMessageMoveFallsBackToCopyAndDelete(t *testing.T) {
	server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS")
	client := server.connect(t)

	if err := client.MessageMove("INBOX", "Trash", NewSequenceSet([]any{1, 2}, true)); nil != err {
		t.Fatalf("MessageMove: %v", err)
	}
	for _, want := range []string{
		`UID COPY 1:2 "Trash"`,
		"UID STORE 1:2 +FLAGS.SILENT",
		"UID EXPUNGE 1:2",
	} {
		if !server.hasCommand(want) {
			t.Errorf("%q was not sent: %v", want, server.commands())
		}
	}
	if server.hasCommand("MOVE") {
		t.Errorf("a server without MOVE must not receive MOVE: %v", server.commands())
	}
}

// TestMessageDeleteExpunges checks the \Deleted store and the two expunge
// variants (UID EXPUNGE for the range, plain EXPUNGE for expunge-all).
func TestMessageDeleteExpunges(t *testing.T) {
	t.Run("range", func(t *testing.T) {
		server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS")
		client := server.connect(t)

		if err := client.MessageDelete("INBOX", NewSequenceSet([]any{1, 2}, true), true); nil != err {
			t.Fatalf("MessageDelete: %v", err)
		}
		if !server.hasCommand("UID STORE 1:2 +FLAGS.SILENT") {
			t.Errorf(`the \Deleted store was not sent: %v`, server.commands())
		}
		if !server.hasCommand("UID EXPUNGE 1:2") {
			t.Errorf("UID EXPUNGE was not sent: %v", server.commands())
		}
		if server.plainExpunge() {
			t.Errorf("only the deleted UIDs may be expunged: %v", server.commands())
		}
	})

	t.Run("expunge_all_on_delete", func(t *testing.T) {
		server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS")
		client := server.connect(t)
		// imap.use_expunge_all_on_delete of application.ini.
		client.Settings.ExpungeAllOnDelete = true

		if err := client.MessageDelete("INBOX", NewSequenceSet([]any{3}, true), true); nil != err {
			t.Fatalf("MessageDelete: %v", err)
		}
		if !server.plainExpunge() {
			t.Errorf("the whole mailbox must be expunged: %v", server.commands())
		}
		if server.hasCommand("UID EXPUNGE") {
			t.Errorf("expunge-all must not send UID EXPUNGE: %v", server.commands())
		}
	})

	t.Run("without_uidplus", func(t *testing.T) {
		server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR")
		client := server.connect(t)

		if err := client.MessageDelete("INBOX", NewSequenceSet([]any{4}, true), false); nil != err {
			t.Fatalf("MessageDelete: %v", err)
		}
		// Without UIDPLUS a range expunge degrades to a plain EXPUNGE.
		if !server.plainExpunge() {
			t.Errorf("EXPUNGE was not sent: %v", server.commands())
		}
	})

	t.Run("invalid_arguments", func(t *testing.T) {
		client := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR").connect(t)

		if err := client.MessageDelete("", NewSequenceSet([]any{1}, true), true); nil == err {
			t.Error("an empty folder must be refused")
		}
		if err := client.MessageDelete("INBOX", NewSequenceSet([]any{"2,3"}, true), true); nil == err {
			t.Error("an invalid range must be refused")
		}
	})
}

// TestFolderExpungeRange checks FolderExpunge()/FolderExpungeRange().
func TestFolderExpungeRange(t *testing.T) {
	server := actionServer(t, "IMAP4rev1 AUTH=PLAIN SASL-IR UIDPLUS")
	client := server.connect(t)

	if err := client.FolderExpunge(); nil != err {
		t.Fatalf("FolderExpunge: %v", err)
	}
	if !server.plainExpunge() {
		t.Errorf("EXPUNGE was not sent: %v", server.commands())
	}

	if err := client.FolderExpungeRange(NewSequenceSet([]any{5, 6}, true)); nil != err {
		t.Fatalf("FolderExpungeRange: %v", err)
	}
	if !server.hasCommand("UID EXPUNGE 5:6") {
		t.Errorf("UID EXPUNGE was not sent: %v", server.commands())
	}

	// A sequence-number range is filtered by the UID check of FolderExpunge.
	if err := client.FolderExpungeRange(NewSequenceSet([]any{7}, false)); nil != err {
		t.Fatalf("FolderExpungeRange (sequence numbers): %v", err)
	}
	if server.hasCommand("UID EXPUNGE 7") {
		t.Errorf("a sequence-number range must not be UID EXPUNGEd: %v", server.commands())
	}
}
