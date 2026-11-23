package mime

import (
	"strings"
	"unicode"
)

// SecureFileName ports MailSo\Base\Utils::SecureFileName(): replace control and
// format characters, the Win95 reserved characters and the path separators with
// "-". It is used by BodyStructure::jsonSerialize() for the attachment names.
func SecureFileName(name string) string {
	return strings.Map(func(character rune) rune {
		switch character {
		case '|', '\\', '?', '*', '<', '"', ':', '>', '+', '[', ']', '/', '&':
			return '-'
		}
		if unicode.In(character, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co) {
			return '-'
		}
		return character
	}, name)
}
