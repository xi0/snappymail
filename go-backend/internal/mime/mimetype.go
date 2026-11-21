// Package mime holds the MIME helpers of the SnappyMail backend.
//
// Step 12 only needs the file type detection of
// SnappyMail\File\MimeType (fromFile/fromFilename/toExtension) for the upload
// and raw endpoints; step 17 adds the MIME parse/build side to this package.
//
// PHP detects a file with finfo (libmagic). Go has no libmagic binding, so the
// magic table below covers the formats the webmail actually handles and falls
// back to the file name exactly like the PHP detectDeeper()/fromFilename()
// chain does. The extension table and its precedence are a compatibility
// contract (the temporary upload names embed the extension), so the map is kept
// byte-for-byte equal to SnappyMail\File\MimeType::$types.
package mime

import (
	"bytes"
	"os"
	"strings"
)

// typeEntry is one row of the PHP $types map. The map is ordered because
// array_search() returns the first key whose value matches, while a duplicate
// key keeps its first position but takes the last value.
type typeEntry struct {
	extension string
	mimeType  string
}

// orderedTypes is SnappyMail\File\MimeType::$types in PHP insertion order with
// the duplicate keys collapsed (first position, last value).
var orderedTypes = buildOrderedTypes([]typeEntry{
	{"7z", "application/x-7z-compressed"},
	{"ai", "application/postscript"},
	{"bat", "application/x-msdownload"},
	{"bz", "application/x-bzip"},
	{"bz2", "application/x-bzip2"},
	{"cab", "application/vnd.ms-cab-compressed"},
	{"chm", "application/vnd.ms-htmlhelp"},
	{"com", "application/x-msdownload"},
	{"deb", "application/x-debian-package"},
	{"dll", "application/x-msdownload"},
	{"doc", "application/msword"},
	{"docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	{"dot", "application/msword"},
	{"dotx", "application/vnd.openxmlformats-officedocument.wordprocessingml.template"},
	{"eps", "application/postscript"},
	{"epub", "application/epub"},
	{"exe", "application/x-msdownload"},
	{"gz", "application/gzip"},
	{"hlp", "application/winhlp"},
	{"js", "application/javascript"},
	{"json", "application/json"},
	{"msi", "application/x-msdownload"},
	{"odp", "application/vnd.oasis.opendocument.presentation"},
	{"ods", "application/vnd.oasis.opendocument.spreadsheet"},
	{"odt", "application/vnd.oasis.opendocument.text"},
	{"ogx", "application/ogg"},
	{"p10", "application/pkcs10"},
	{"p7c", "application/pkcs7-mime"},
	{"p7m", "application/pkcs7-mime"},
	{"p7s", "application/pkcs7-signature"},
	{"pdf", "application/pdf"},
	// The PHP array assigns 'php' twice; the second value wins.
	{"php", "application/x-httpd-php"},
	{"php", "application/x-php"},
	{"ppt", "application/vnd.ms-powerpoint"},
	{"pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
	{"ps", "application/postscript"},
	{"psd", "image/vnd.adobe.photoshop"},
	{"rar", "application/rar-compressed"},
	{"rar", "application/x-rar-compressed"},
	{"rtf", "application/rtf"},
	{"scr", "application/x-msdownload"},
	{"sql", "application/sql"},
	{"swf", "application/shockwave-flash"},
	{"swf", "application/x-shockwave-flash"},
	{"tar", "application/gtar"},
	{"tnef", "application/vnd.ms-tnef"},
	{"torrent", "application/x-bittorrent"},
	{"wgt", "application/widget"},
	{"xls", "application/vnd.ms-excel"},
	{"xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	{"xltx", "application/vnd.openxmlformats-officedocument.spreadsheetml.template"},
	{"zip", "application/zip"},

	{"aac", "audio/aac"},
	{"aif", "audio/aiff"},
	{"aifc", "audio/aiff"},
	{"aiff", "audio/aiff"},
	{"flac", "audio/flac"},
	{"m3u", "audio/x-mpegurl"},
	{"midi", "audio/midi"},
	{"mp3", "audio/mpeg"},
	{"mp4a", "audio/mp4"},
	{"ogg", "audio/ogg"},
	{"wav", "audio/wav"},
	{"weba", "audio/webm"},

	{"ttf", "font/ttf"},
	{"woff", "font/woff"},
	{"woff2", "font/woff2"},

	{"bmp", "image/bmp"},
	{"cgm", "image/cgm"},
	{"djv", "image/vnd.djvu"},
	{"djvu", "image/vnd.djvu"},
	{"gif", "image/gif"},
	{"ico", "image/vnd.microsoft.icon"},
	{"ief", "image/ief"},
	{"jpeg", "image/jpeg"},
	{"jfif", "image/jpeg"},
	{"jpe", "image/jpeg"},
	{"jpg", "image/jpeg"},
	{"png", "image/png"},
	{"svg", "image/svg+xml"},
	{"svgz", "image/svg+xml"},
	{"tiff", "image/tiff"},
	{"tif", "image/tiff"},
	{"webp", "image/webp"},

	{"eml", "message/rfc822"},
	{"mime", "message/rfc822"},
	{"u8dsn", "message/global-delivery-status"},

	{"txt", "text/plain"},
	{"asp", "text/asp"},
	{"cfg", "text/plain"},
	{"conf", "text/plain"},
	{"css", "text/css"},
	{"csv", "text/csv"},
	{"def", "text/plain"},
	{"html", "text/html"},
	{"htm", "text/html"},
	{"ics", "text/calendar"},
	{"ifb", "text/calendar"},
	{"in", "text/plain"},
	{"ini", "text/plain"},
	{"list", "text/plain"},
	{"log", "text/plain"},
	{"pl", "text/perl"},
	{"rtx", "text/richtext"},
	{"text", "text/plain"},
	{"vcf", "text/vcard"},
	{"vcard", "text/vcard"},
	{"xml", "text/xml"},

	{"3g2", "video/3gpp2"},
	{"3gp", "video/3gpp"},
	{"asf", "video/x-ms-asf"},
	{"asx", "video/x-ms-asf"},
	{"avi", "video/x-msvideo"},
	{"flv", "video/flv"},
	{"h261", "video/h261"},
	{"h263", "video/h263"},
	{"h264", "video/h264"},
	{"jpgv", "video/jpgv"},
	{"m4v", "video/x-m4v"},
	{"mov", "video/quicktime"},
	{"movie", "video/x-sgi-movie"},
	{"mp4", "video/mp4"},
	{"mp4v", "video/mp4"},
	{"mpeg", "video/mpeg"},
	{"m1v", "video/mpeg"},
	{"m2v", "video/mpeg"},
	{"mpe", "video/mpeg"},
	{"mpg", "video/mpeg"},
	{"mpg4", "video/mp4"},
	{"ogv", "video/ogg"},
	{"qt", "video/quicktime"},
	{"webm", "video/webm"},
	{"wm", "video/x-ms-wm"},
	{"wmv", "video/x-ms-wmv"},
	{"wmx", "video/x-ms-wmx"},
	{"wvx", "video/x-ms-wvx"},
})

// typeByExtension is the PHP $types lookup of fromFilename(): a duplicate
// extension keeps its last value.
var typeByExtension = buildExtensionMap(orderedTypes)

// buildOrderedTypes collapses duplicate extensions the way PHP arrays do: the
// key keeps its first position but takes the last assigned value.
func buildOrderedTypes(entries []typeEntry) []typeEntry {
	result := make([]typeEntry, 0, len(entries))
	position := make(map[string]int, len(entries))

	for _, entry := range entries {
		if index, ok := position[entry.extension]; ok {
			result[index].mimeType = entry.mimeType
			continue
		}
		position[entry.extension] = len(result)
		result = append(result, entry)
	}

	return result
}

// buildExtensionMap indexes the ordered entries by extension.
func buildExtensionMap(entries []typeEntry) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		result[entry.extension] = entry.mimeType
	}
	return result
}

// FromFilename is MimeType::fromFilename(): the MIME type of a file name, using
// its lowercased extension (winmail.dat is the TNEF case).
func FromFilename(filename string) string {
	filename = strings.ToLower(filename)
	if "winmail.dat" == filename {
		return typeByExtension["tnef"]
	}

	extension := filename
	if index := strings.LastIndex(filename, "."); 0 <= index {
		extension = filename[index+1:]
	} else {
		extension = filename
	}

	return typeByExtension[extension]
}

// ToExtension is MimeType::toExtension(): the extension of a MIME type with a
// leading dot (".asc" for the PGP types, ".bin" when unknown).
func ToExtension(mimeType string) string {
	mimeType = strings.ToLower(mimeType)

	extension := ""
	if "application/pgp-signature" == mimeType || "application/pgp-keys" == mimeType {
		extension = "asc"
	} else {
		normalized := strings.ReplaceAll(mimeType, "application/x-tar", "application/gtar")
		extension = firstExtension(normalized)
		if "" == extension {
			extension = firstExtension(strings.ReplaceAll(normalized, "/x-", "/"))
		}
		if "" == extension {
			extension = firstExtension(strings.ReplaceAll(normalized, "/", "/x-"))
		}
		if "" == extension {
			extension = "bin"
		}
	}

	return "." + extension
}

// firstExtension is array_search($mime, $types): the first extension that maps
// to the MIME type.
func firstExtension(mimeType string) string {
	for _, entry := range orderedTypes {
		if entry.mimeType == mimeType {
			return entry.extension
		}
	}
	return ""
}

// FromFile is MimeType::fromFile(): inspect the beginning of a file and fall
// back to the generic type resolution with the file name.
func FromFile(filename, name string) string {
	handle, err := os.Open(filename)
	if nil != err {
		return ""
	}
	defer handle.Close()

	header := make([]byte, 4096)
	read, _ := handle.Read(header)
	if 0 == read {
		return ""
	}

	return FromContent(header[:read], nameOrFilename(name, filename))
}

// FromContent is MimeType::fromStream()/fromString(): detect the type from the
// leading bytes, using the name for the generic types.
func FromContent(data []byte, name string) string {
	mimeType := fromData(data)
	if "" == mimeType {
		return ""
	}
	return detectDeeper(mimeType, name)
}

// detectDeeper is MimeType::detectDeeper(): a generic container type is refined
// by the file name (e.g. a ".docx" zip becomes the OOXML document type).
func detectDeeper(mimeType, name string) string {
	switch mimeType {
	case "application/ogg",
		"application/vnd.ms-office",
		"application/octet-stream",
		"application/zip":
		if refined := FromFilename(name); "" != refined {
			return refined
		}
	}
	return mimeType
}

// nameOrFilename is "$name ?: $filename" of MimeType::fromFile().
func nameOrFilename(name, filename string) string {
	if "" != name {
		return name
	}
	return filename
}

// fromData is MimeType::getFromData(): the magic signatures Go can recognise
// without libmagic.
func fromData(data []byte) string {
	if bytes.Contains(data, []byte("-----BEGIN PGP SIGNATURE-----")) {
		return "application/pgp-signature"
	}
	if bytes.Contains(data, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) ||
		bytes.Contains(data, []byte("-----BEGIN PGP PRIVATE KEY BLOCK-----")) {
		return "application/pgp-keys"
	}

	switch {
	case hasPrefix(data, 0xFF, 0xD8, 0xFF):
		return "image/jpeg"
	case hasPrefix(data, 0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A):
		return "image/png"
	case hasPrefix(data, 'G', 'I', 'F', '8'):
		return "image/gif"
	case hasPrefix(data, '%', 'P', 'D', 'F', '-'):
		return "application/pdf"
	case hasPrefix(data, 'P', 'K', 0x03, 0x04), hasPrefix(data, 'P', 'K', 0x05, 0x06):
		return "application/zip"
	case hasPrefix(data, 0x1F, 0x8B):
		return "application/gzip"
	case hasPrefix(data, 'B', 'Z', 'h'):
		return "application/x-bzip2"
	case hasPrefix(data, 0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C):
		return "application/x-7z-compressed"
	case hasPrefix(data, 'R', 'a', 'r', '!', 0x1A, 0x07):
		return "application/x-rar-compressed"
	case hasPrefix(data, 'O', 'g', 'g', 'S'):
		return "application/ogg"
	case hasPrefix(data, 'B', 'M'):
		return "image/bmp"
	case hasPrefix(data, 'I', 'I', 0x2A, 0x00), hasPrefix(data, 'M', 'M', 0x00, 0x2A):
		return "image/tiff"
	case hasPrefix(data, 0x00, 0x00, 0x01, 0x00):
		return "image/vnd.microsoft.icon"
	case hasPrefix(data, 'I', 'D', '3'):
		return "audio/mpeg"
	case hasPrefix(data, 0xFF, 0xFB), hasPrefix(data, 0xFF, 0xF3), hasPrefix(data, 0xFF, 0xF2):
		return "audio/mpeg"
	case hasPrefix(data, 'f', 'L', 'a', 'C'):
		return "audio/flac"
	case hasPrefix(data, 'R', 'I', 'F', 'F'):
		return riffType(data)
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return "video/mp4"
	}

	return ""
}

// riffType refines a RIFF container by its four character form type.
func riffType(data []byte) string {
	if len(data) < 12 {
		return "application/octet-stream"
	}
	switch string(data[8:12]) {
	case "WEBP":
		return "image/webp"
	case "WAVE":
		return "audio/wav"
	case "WEBM":
		return "video/webm"
	}
	return "application/octet-stream"
}

// hasPrefix reports whether data begins with the given bytes.
func hasPrefix(data []byte, prefix ...byte) bool {
	if len(data) < len(prefix) {
		return false
	}
	for index, value := range prefix {
		if data[index] != value {
			return false
		}
	}
	return true
}
