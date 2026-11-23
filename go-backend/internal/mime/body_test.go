package mime

// Tests of the Step 17 body decoding helpers: the charset aliases of
// Utils::NormalizeCharset(), the conversions, the transfer encodings and the
// flowed-text unwrapping.

import (
	"encoding/base64"
	"testing"
)

func TestNormalizeCharset(t *testing.T) {
	tests := []struct {
		value       string
		asciiAsUtf8 bool
		want        string
	}{
		{"UTF-8", false, "utf-8"},
		{"utf8", false, "utf-8"},
		{"us-ascii", false, "iso-8859-1"},
		{"US-ASCII", true, "utf-8"},
		{"ISO-8859-1", false, "iso-8859-1"},
		{"iso8859-1", false, "iso-8859-1"},
		{"cp1252", false, "windows-1252"},
		{"CP1252", false, "windows-1252"},
		{"windows-1252", false, "windows-1252"},
		{"windows1252", false, "windows-1252"},
		{"x-gbk", false, "gb2312"},
		{"utf7imap", false, "utf7-imap"},
		{"ks_c_5601-1987", false, "euc-kr"},
		{"unicode-1-1-utf-7", false, "utf-7"},
		{"iso-8859-8-i", false, "iso-8859-8"},
		{"windows-286", false, "windows-286"},
	}

	for _, test := range tests {
		if got := NormalizeCharset(test.value, test.asciiAsUtf8); got != test.want {
			t.Errorf("NormalizeCharset(%q, %v) = %q, want %q", test.value, test.asciiAsUtf8, got, test.want)
		}
	}
}

func TestConvertEncoding(t *testing.T) {
	// A windows-1252 byte becomes UTF-8.
	if got := ConvertEncoding("Caf\xe9", "windows-1252", "utf-8"); "Caf\u00e9" != got {
		t.Errorf("windows-1252 conversion = %q", got)
	}

	// An empty (or whitespace only) value is returned unchanged.
	if got := ConvertEncoding("  ", "utf-8", "utf-8"); "  " != got {
		t.Errorf("blank value = %q", got)
	}

	// A same-charset pair that is not UTF-8 is a no-op, even for bytes that are
	// invalid in that charset (PHP returns the input then).
	if got := ConvertEncoding("\xff", "iso-8859-1", "iso-8859-1"); "\xff" != got {
		t.Errorf("same charset = %q", got)
	}

	// An unsupported charset leaves the value alone.
	if got := ConvertEncoding("abc", "x-unknown-charset", "utf-8"); "abc" != got {
		t.Errorf("unknown charset = %q", got)
	}

	// Valid UTF-8 is passed through unchanged.
	if got := ConvertEncoding("Grüße", "utf-8", "utf-8"); "Grüße" != got {
		t.Errorf("utf-8 identity = %q", got)
	}
}

func TestDecodeEncodingValue(t *testing.T) {
	// quoted-printable: "=C3=A9" is a UTF-8 é, "=20" a space and "=\r\n" a soft
	// line break.
	if got := DecodeEncodingValue("Caf=C3=A9=20noir=\r\n!", "quoted-printable"); "Café noir!" != got {
		t.Errorf("quoted-printable = %q", got)
	}
	// The short aliases PHP accepts.
	if got := DecodeEncodingValue("a=3Db", "Q"); "a=b" != got {
		t.Errorf("quoted-printable (Q) = %q", got)
	}
	if got := DecodeEncodingValue("a=3Db", "quoted_printable"); "a=b" != got {
		t.Errorf("quoted-printable (quoted_printable) = %q", got)
	}

	// base64 (with and without the trailing padding).
	encoded := base64.StdEncoding.EncodeToString([]byte("Hello <b>there</b>"))
	if got := DecodeEncodingValue(encoded, "base64"); "Hello <b>there</b>" != got {
		t.Errorf("base64 = %q", got)
	}
	if got := DecodeEncodingValue("SGVsbG8=", "B"); "Hello" != got {
		t.Errorf("base64 (B) = %q", got)
	}

	// 7bit/8bit/nothing stays as it is.
	if got := DecodeEncodingValue("plain =text", "7bit"); "plain =text" != got {
		t.Errorf("7bit = %q", got)
	}
	// An invalid "=" sequence is kept, like quoted_printable_decode().
	if got := DecodeEncodingValue("a=zzb", "quoted-printable"); "a=zzb" != got {
		t.Errorf("invalid escape = %q", got)
	}
}

func TestBase64DecodeIsTolerant(t *testing.T) {
	// PHP's second pass drops stray characters and pads the remainder.
	if got, err := Base64Decode("SGVs bG8*"); nil != err || "Hello" != got {
		t.Errorf("Base64Decode = %q, %v", got, err)
	}
}

func TestDecodeFlowedFormat(t *testing.T) {
	// " " + LF is a soft line break and becomes a single space.
	if got := DecodeFlowedFormat("a \nb \r\nc"); "a b c" != got {
		t.Errorf("flowed = %q", got)
	}
	// A line break without the trailing space is a hard break.
	if got := DecodeFlowedFormat("a\nb"); "a\nb" != got {
		t.Errorf("hard break = %q", got)
	}
}
