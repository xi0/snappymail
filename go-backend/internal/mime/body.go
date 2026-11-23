package mime

// The body decoding helpers of MailSo\Base\Utils that the message view needs:
// transfer-encoding decoding (base64 / quoted-printable), charset conversion and
// the flowed-text unwrapping. They are ported as-is because the decoded body is
// what the frontend renders.
//
// Step 17 is the parse side only: HtmlUtils::BuildHtml()/ConvertHtmlToPlain()
// (the build side of a composed mail) belong to step 19.

import (
	"encoding/base64"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// The charset names of MailSo\Base\Enumerations\Charset the helpers compare to.
const (
	// CharsetUTF8 is Enumerations\Charset::UTF_8.
	CharsetUTF8 = "utf-8"
	// CharsetISO88591 is Enumerations\Charset::ISO_8859_1.
	CharsetISO88591 = "iso-8859-1"
)

// NormalizeCharset is Utils::NormalizeCharset(): lowercase the name, map the
// aliases PHP knows and canonicalise the "cp1252"/"windows-1252" spelling.
func NormalizeCharset(value string, asciiAsUTF8 bool) string {
	value = strings.ToLower(value)
	if strings.HasPrefix(value, "iso8") {
		value = "iso-8" + value[4:]
	}

	switch value {
	case "asci", "ascii", "us-asci", "us-ascii":
		if asciiAsUTF8 {
			return CharsetUTF8
		}
		return CharsetISO88591

	case "unicode-1-1-utf-7", "unicode-1-utf-7", "unicode-utf-7":
		return "utf-7"

	case "utf8", "utf-8":
		return CharsetUTF8

	case "utf7imap", "utf-7imap", "utf7-imap", "utf-7-imap":
		return "utf7-imap"

	case "ks-c-5601-1987", "ks_c_5601-1987", "ks_c_5601_1987":
		return "euc-kr"

	case "x-gbk":
		return "gb2312"

	case "iso-8859-i", "iso-8859-8-i":
		return "iso-8859-8"
	}

	// preg_replace('/^(cp|windows)-?(12[\d]*)/i', 'windows-$2', $sEncoding):
	// the matched prefix ("cp"/"windows" and an optional "-") is replaced, the
	// rest of the name is kept verbatim.
	for _, prefix := range []string{"windows-", "windows", "cp"} {
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		if rest := value[len(prefix):]; strings.HasPrefix(rest, "12") {
			return "windows-" + rest
		}
	}

	return value
}

// ConvertEncoding is Utils::ConvertEncoding(): convert value from one charset to
// another, leaving an empty value and an identical (non UTF-8) pair untouched.
func ConvertEncoding(value, from, to string) string {
	from = NormalizeCharset(from, false)
	to = NormalizeCharset(to, false)

	if "" == strings.TrimSpace(value) || (from == to && CharsetUTF8 != from) {
		return value
	}

	return MbConvertEncoding(value, from, to)
}

// MbConvertEncoding is Utils::MbConvertEncoding(): convert with whatever the
// standard library knows and return the input otherwise (PHP falls back to
// iconv and finally to the unchanged string).
func MbConvertEncoding(value, from, to string) string {
	if "" == from {
		return value
	}

	// The only renamed encoding of Utils::$RenameEncoding.
	if "shift_jis" == from {
		from = "sjis"
	}

	encoding, err := htmlindex.Get(from)
	if nil != err {
		return value
	}
	decoded, err := encoding.NewDecoder().String(value)
	if nil != err {
		return value
	}

	// The decoders of x/text produce UTF-8; asking for another target charset
	// (the only call site asks for UTF-8) falls back to the input, like PHP
	// does for an unsupported pair.
	if target := NormalizeCharset(to, false); CharsetUTF8 != target {
		if encoder, encodeErr := htmlindex.Get(target); nil == encodeErr {
			if encoded, encodeStringErr := encoder.NewEncoder().String(decoded); nil == encodeStringErr {
				return encoded
			}
		}
	}

	return decoded
}

// DecodeEncodingValue is Utils::DecodeEncodingValue(): apply a
// Content-Transfer-Encoding to a body part.
func DecodeEncodingValue(value, encodingType string) string {
	switch strings.ToLower(strings.TrimSpace(encodingType)) {
	case "q", "quoted_printable", "quoted-printable":
		return quotedPrintableDecode(value)
	case "b", "base64":
		decoded, err := Base64Decode(value)
		if nil != err {
			return value
		}
		return decoded
	}
	return value
}

// DecodeFlowedFormat is Utils::DecodeFlowedFormat(): join the soft line breaks of
// RFC 3676 (format=flowed) text.
func DecodeFlowedFormat(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		if ' ' == character {
			if index+1 < len(value) && '\n' == value[index+1] {
				builder.WriteByte(' ')
				index++
				continue
			}
			if index+2 < len(value) && '\r' == value[index+1] && '\n' == value[index+2] {
				builder.WriteByte(' ')
				index += 2
				continue
			}
		}
		builder.WriteByte(character)
	}
	return builder.String()
}

// Base64Decode is Utils::Base64Decode(): strict base64_decode(), with the
// tolerant second pass of PHP for base64 that carries stray characters or
// missing padding.
func Base64Decode(value string) (string, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); nil == err {
		return string(decoded), nil
	}

	cleaned := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case 'A' <= character && 'Z' >= character,
			'a' <= character && 'z' >= character,
			'0' <= character && '9' >= character,
			'+' == character, '/' == character, '=' == character:
			cleaned = append(cleaned, character)
		}
	}
	if remainder := len(cleaned) % 4; 0 != remainder {
		cleaned = append(cleaned, []byte(strings.Repeat("=", 4-remainder))...)
	}

	decoded, err := base64.StdEncoding.DecodeString(string(cleaned))
	if nil != err {
		return "", err
	}
	return string(decoded), nil
}

// quotedPrintableDecode is PHP's quoted_printable_decode(): "=XX" becomes the
// byte XX, "=\r\n" and "=\n" are soft line breaks and anything else after an "="
// stays as it is.
func quotedPrintableDecode(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))

	for index := 0; index < len(value); index++ {
		character := value[index]
		if '=' != character {
			builder.WriteByte(character)
			continue
		}

		if index+1 < len(value) && '\n' == value[index+1] {
			index++
			continue
		}
		if index+2 < len(value) && '\r' == value[index+1] && '\n' == value[index+2] {
			index += 2
			continue
		}
		if index+2 < len(value) {
			high, highOK := unhex(value[index+1])
			low, lowOK := unhex(value[index+2])
			if highOK && lowOK {
				builder.WriteByte(high<<4 | low)
				index += 2
				continue
			}
		}

		builder.WriteByte(character)
	}

	return builder.String()
}

// unhex decodes one hexadecimal digit.
func unhex(character byte) (byte, bool) {
	switch {
	case '0' <= character && '9' >= character:
		return character - '0', true
	case 'a' <= character && 'f' >= character:
		return character - 'a' + 10, true
	case 'A' <= character && 'F' >= character:
		return character - 'A' + 10, true
	}
	return 0, false
}
