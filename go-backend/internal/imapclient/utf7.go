package imapclient

// Modified UTF-7 (RFC 3501 §5.1.3), the mailbox name encoding of IMAP: the
// counterpart of MailSo\Base\Utils::Utf8ToUtf7Modified()/Utf7ModifiedToUtf8().
//
// Folder names travel in modified UTF-7 unless the server negotiated
// UTF8=ACCEPT/UTF8=ONLY, so every name that crosses the wire goes through these
// two helpers (ImapClient::EscapeFolderName()/toUTF8()).

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

// mUTF7Encoder is the standard base64 alphabet with the modified-UTF-7
// substitution of ',' for '/'.
var mUTF7Encoding = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,").WithPadding(base64.NoPadding)

// UTF8ToMUTF7 encodes a UTF-8 mailbox name as modified UTF-7.
func UTF8ToMUTF7(value string) string {
	var builder strings.Builder

	// run collects the characters that have to be base64 encoded.
	var run []rune
	flush := func() {
		if 0 == len(run) {
			return
		}
		units := utf16.Encode(run)
		raw := make([]byte, 0, 2*len(units))
		for _, unit := range units {
			raw = append(raw, byte(unit>>8), byte(unit))
		}
		builder.WriteByte('&')
		builder.WriteString(mUTF7Encoding.EncodeToString(raw))
		builder.WriteByte('-')
		run = run[:0]
	}

	for _, character := range value {
		if '&' == character {
			flush()
			builder.WriteString("&-")
			continue
		}
		if 0x20 <= character && 0x7e >= character {
			flush()
			builder.WriteRune(character)
			continue
		}
		run = append(run, character)
	}
	flush()

	return builder.String()
}

// MUTF7ToUTF8 decodes a modified UTF-7 mailbox name into UTF-8. A malformed
// sequence is passed through unchanged, so a name is never lost.
func MUTF7ToUTF8(value string) string {
	var builder strings.Builder

	for index := 0; index < len(value); {
		if '&' != value[index] {
			builder.WriteByte(value[index])
			index++
			continue
		}

		end := strings.IndexByte(value[index:], '-')
		if 0 > end {
			// Unterminated sequence: keep the rest verbatim.
			builder.WriteString(value[index:])
			break
		}
		encoded := value[index+1 : index+end]
		index += end + 1

		if "" == encoded {
			builder.WriteByte('&')
			continue
		}

		raw, err := mUTF7Encoding.DecodeString(encoded)
		if nil != err || 1 == len(raw)%2 {
			builder.WriteString("&" + encoded + "-")
			continue
		}

		units := make([]uint16, 0, len(raw)/2)
		for offset := 0; offset < len(raw); offset += 2 {
			units = append(units, uint16(raw[offset])<<8|uint16(raw[offset+1]))
		}
		builder.WriteString(string(utf16.Decode(units)))
	}

	return builder.String()
}
