package mime

// Tests of Step 19: the byte layout of a composed message, the header order of
// Message::ToStream(), Bcc stripping, the multipart assembly and the body
// encodings.

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// fixedOptions is a deterministic builder configuration for the tests.
func fixedOptions() BuildOptions {
	return BuildOptions{
		Version:        "0.0.0",
		BoundaryPrefix: "_Part_",
		Now: func() time.Time {
			return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
		},
		Random: func(buffer []byte) (int, error) {
			for index := range buffer {
				buffer[index] = 0
			}
			return len(buffer), nil
		},
	}
}

// TestBuiltMessageHeaderOrder checks the setDefaultHeaders block: the generated
// values are prepended in reverse assignment order, so MIME-Version comes first.
func TestBuiltMessageHeaderOrder(t *testing.T) {
	message := NewBuiltMessage(fixedOptions())
	message.SetFrom(ParseEmail(`"Alice" <alice@example.com>`))
	message.RegenerateMessageID("example.com")
	message.SetTo(NewEmailCollection("bob@example.com"))
	message.SetSubject("Hello")
	message.AddPlain("Hello Bob")

	document := string(message.ToBytes(false))

	for _, want := range []string{
		"MIME-Version: 1.0\r\n",
		"X-Mailer: SnappyMail/0.0.0\r\n",
		"Date: Tue, 02 Jan 2024 03:04:05 +0000\r\n",
		`From: "Alice" <alice@example.com>`,
		"Message-ID: <",
		"To: bob@example.com",
		"Subject: Hello",
		"Content-Type: text/plain; charset=\"utf-8\"",
		"Content-Transfer-Encoding: quoted-printable",
		"\r\n\r\nHello Bob",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("message is missing %q:\n%s", want, document)
		}
	}

	// The custom headers follow the root Content-Type in buildMessage() order.
	if strings.Index(document, "Content-Type: text/plain") > strings.Index(document, "From:") {
		t.Errorf("the root Content-Type must precede the stored headers:\n%s", document)
	}
}

// TestBuiltMessageStripsBcc checks ToStream(true).
func TestBuiltMessageStripsBcc(t *testing.T) {
	message := NewBuiltMessage(fixedOptions())
	message.SetFrom(ParseEmail("alice@example.com"))
	message.RegenerateMessageID("example.com")
	message.SetTo(NewEmailCollection("bob@example.com"))
	message.SetBcc(NewEmailCollection("secret@example.com"))
	message.SetSubject("Hi")
	message.AddPlain("body")

	if strings.Contains(string(message.ToBytes(true)), "Bcc:") {
		t.Error("ToBytes(true) must not render Bcc")
	}
	if !strings.Contains(string(message.ToBytes(false)), "Bcc: secret@example.com") {
		t.Error("ToBytes(false) must render Bcc")
	}
}

// TestBuiltMessageHTMLAlternative checks the multipart/alternative assembly.
func TestBuiltMessageHTMLAlternative(t *testing.T) {
	message := NewBuiltMessage(fixedOptions())
	message.SetFrom(ParseEmail("alice@example.com"))
	message.RegenerateMessageID("example.com")
	message.SetTo(NewEmailCollection("bob@example.com"))
	message.AddHTML("<p>Hello</p>", "Hello")

	document := string(message.ToBytes(false))

	for _, want := range []string{
		"Content-Type: multipart/alternative;",
		"boundary=\"_Part_",
		"Content-Type: text/plain; charset=\"utf-8\"",
		"Content-Type: text/html; charset=\"utf-8\"",
		"<p>Hello</p>",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("message is missing %q:\n%s", want, document)
		}
	}
	// Both parts are quoted-printable.
	if 2 != strings.Count(document, "Content-Transfer-Encoding: quoted-printable") {
		t.Errorf("the alternative parts must be quoted-printable:\n%s", document)
	}
}

// TestBuiltMessageAttachment checks Attachment::ToPart() and the base64 body.
func TestBuiltMessageAttachment(t *testing.T) {
	message := NewBuiltMessage(fixedOptions())
	message.SetFrom(ParseEmail("alice@example.com"))
	message.RegenerateMessageID("example.com")
	message.SetTo(NewEmailCollection("bob@example.com"))
	message.AddPlain("body")
	message.AddAttachment(&BuildAttachment{
		Data:            []byte("attachment data"),
		FileName:        "note.txt",
		ContentType:     "text/plain",
		Inline:          false,
		ContentID:       "<cid@example.com>",
		ContentLocation: "https://example.com/note.txt",
	})

	document := string(message.ToBytes(false))

	for _, want := range []string{
		"Content-Type: multipart/mixed",
		"Content-Type: text/plain; name=\"note.txt\"",
		"Content-Disposition: attachment; filename=\"note.txt\"",
		"Content-ID: <cid@example.com>",
		"Content-Location: https://example.com/note.txt",
		"Content-Transfer-Encoding: base64",
		"YXR0YWNobWVudCBkYXRh",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("message is missing %q:\n%s", want, document)
		}
	}
}

// TestEncodeQuotedPrintable checks the soft line breaks and the = escapes.
func TestEncodeQuotedPrintable(t *testing.T) {
	if got := EncodeQuotedPrintable("a=b"); "a=3Db" != got {
		t.Errorf("EncodeQuotedPrintable = %q", got)
	}

	line := strings.Repeat("a", 100)
	encoded := EncodeQuotedPrintable(line)
	if !strings.Contains(encoded, "=\r\n") {
		t.Errorf("a long line must be soft wrapped: %q", encoded)
	}
	for _, part := range strings.Split(encoded, "\r\n") {
		if 74 < len(part) {
			t.Errorf("encoded line is %d bytes: %q", len(part), part)
		}
	}
}

// TestEncodeBase64 checks the 74 character base64 wrapping.
func TestEncodeBase64(t *testing.T) {
	encoded := EncodeBase64([]byte(strings.Repeat("a", 120)))
	for _, line := range strings.Split(encoded, "\r\n") {
		if 74 < len(line) {
			t.Errorf("base64 line is %d bytes: %q", len(line), line)
		}
	}
}

// TestEncodeMimeSubject checks the iconv_mime_encode() line budget: the first
// line has the "Subject: " prefix, continuation lines a leading space, and the
// encoded words stay within 69 characters (line-length 74 minus the 5 reserved).
func TestEncodeMimeSubject(t *testing.T) {
	value := "Hëllo wörld this is a reasonably long subject line that needs folding"
	encoded := encodeMimeSubject("Subject", value)

	lines := strings.Split(encoded, "\r\n")
	if 2 != len(lines) {
		t.Fatalf("encoded %d lines: %q", len(lines), encoded)
	}
	for _, line := range lines {
		if 69 < len(line) {
			t.Errorf("encoded line is %d bytes: %q", len(line), line)
		}
	}
	if !strings.HasPrefix(lines[0], "Subject: =?UTF-8?B?") {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], " =?UTF-8?B?") {
		t.Errorf("continuation line = %q", lines[1])
	}

	// Decode the words to make sure no byte was lost.
	var decoded strings.Builder
	for _, line := range lines {
		start := strings.Index(line, "?B?")
		end := strings.LastIndex(line, "?=")
		if 0 > start || end <= start {
			t.Fatalf("not an encoded word: %q", line)
		}
		raw, err := base64.StdEncoding.DecodeString(line[start+3 : end])
		if nil != err {
			t.Fatalf("decode: %v", err)
		}
		decoded.Write(raw)
	}
	if value != decoded.String() {
		t.Errorf("decoded = %q, want %q", decoded.String(), value)
	}
}

// TestEncodeHeaderValue checks that only non-ASCII values are encoded.
func TestEncodeHeaderValue(t *testing.T) {
	if got := EncodeHeaderValue("plain"); "plain" != got {
		t.Errorf("EncodeHeaderValue(plain) = %q", got)
	}
	if got := EncodeHeaderValue("Ünïcode"); !strings.HasPrefix(got, "=?utf-8?B?") {
		t.Errorf("EncodeHeaderValue(non-ascii) = %q", got)
	}
}
