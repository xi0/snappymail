package mime

import "testing"

func TestFromFilename(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"photo.JPG", "image/jpeg"},
		{"report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"sheet.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"notes.txt", "text/plain"},
		{"archive.tar", "application/gtar"},
		{"winmail.dat", "application/vnd.ms-tnef"},
		{"noextension", ""},
		{"unknown.xyz", ""},
	}

	for _, test := range tests {
		if got := FromFilename(test.name); got != test.want {
			t.Errorf("FromFilename(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestToExtension(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/jpeg", ".jpeg"},
		{"image/png", ".png"},
		{"text/plain", ".txt"},
		{"application/zip", ".zip"},
		{"application/x-php", ".php"},
		{"application/pgp-keys", ".asc"},
		{"application/pgp-signature", ".asc"},
		{"application/x-unknown", ".bin"},
	}

	for _, test := range tests {
		if got := ToExtension(test.mime); got != test.want {
			t.Errorf("ToExtension(%q) = %q, want %q", test.mime, got, test.want)
		}
	}
}

func TestFromContentMagic(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}
	if got := FromContent(png, "whatever.bin"); "image/png" != got {
		t.Errorf("PNG magic = %q, want image/png", got)
	}

	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}
	if got := FromContent(jpeg, "whatever"); "image/jpeg" != got {
		t.Errorf("JPEG magic = %q, want image/jpeg", got)
	}

	// A zip is refined by the file name (detectDeeper).
	zip := []byte{'P', 'K', 0x03, 0x04, 0x00}
	if got := FromContent(zip, "letter.docx"); got != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("zip+docx = %q, want the OOXML document type", got)
	}
	if got := FromContent(zip, "archive.zip"); "application/zip" != got {
		t.Errorf("zip+zip = %q, want application/zip", got)
	}

	// Unknown content is not guessed.
	if got := FromContent([]byte("plain text"), "file.xyz"); "" != got {
		t.Errorf("unknown = %q, want empty", got)
	}

	// PGP armour is recognised regardless of the name.
	if got := FromContent([]byte("-----BEGIN PGP PUBLIC KEY BLOCK-----"), ""); "application/pgp-keys" != got {
		t.Errorf("PGP key = %q, want application/pgp-keys", got)
	}
}
