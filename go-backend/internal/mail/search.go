package mail

// MailSo\Imap\Enumerations\SortCriteria and MailSo\Imap\SearchCriterias: the
// strict SORT whitelist and the search string parser of MessageList().
//
// Both guard the IMAP command against user supplied values: only the enum based
// sort criteria are ever sent unquoted, and every search term is quoted with
// EscapeString()/LITERAL+.

import (
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mime"
)

// The SORT criteria of MailSo\Imap\Enumerations\SortCriteria.
const (
	SortArrival     = "ARRIVAL"
	SortCc          = "CC"
	SortDate        = "DATE"
	SortFrom        = "FROM"
	SortSize        = "SIZE"
	SortSubject     = "SUBJECT"
	SortTo          = "TO"
	SortDisplayFrom = "DISPLAYFROM"
	SortDisplayTo   = "DISPLAYTO"
	SortReverse     = "REVERSE"
)

// sortDefaultCriteria is SortCriteria::DEFAULT_CRITERIA.
var sortDefaultCriteria = []string{SortArrival, SortCc, SortDate, SortFrom, SortSize, SortSubject, SortTo}

// sortDisplayCriteria is SortCriteria::DISPLAY_CRITERIA.
var sortDisplayCriteria = []string{SortDisplayFrom, SortDisplayTo}

// sortTokenize splits a sort criteria string into upper case tokens.
func sortTokenize(sort string) []string {
	return strings.Fields(strings.ToUpper(strings.TrimSpace(sort)))
}

// ValidateSortCriteria is SortCriteria::validate().
func ValidateSortCriteria(sort string, display bool) bool {
	allowed := make(map[string]struct{}, len(sortDefaultCriteria)+len(sortDisplayCriteria))
	for _, criterion := range sortDefaultCriteria {
		allowed[criterion] = struct{}{}
	}
	if display {
		for _, criterion := range sortDisplayCriteria {
			allowed[criterion] = struct{}{}
		}
	}

	reverse := false
	for _, criterion := range sortTokenize(sort) {
		if SortReverse == criterion {
			if reverse {
				return false
			}
			reverse = true
		} else if _, ok := allowed[criterion]; ok {
			reverse = false
		} else {
			return false
		}
	}
	return !reverse
}

// NormalizeSortCriteria is SortCriteria::normalize().
func NormalizeSortCriteria(sort string, display bool) string {
	if !ValidateSortCriteria(sort, display) {
		return ""
	}
	return strings.Join(sortTokenize(sort), " ")
}

// searchCriteriaRegex is SearchCriterias::RegEx.
const searchCriteriaRegex = `in|e?mail|from|to|subject|has|is|date|since|before|text|body|size|larger|bigger|smaller|maxsize|minsize|keyword|older_than|newer_than|on|senton|sentsince|sentbefore|header`

// SearchCriterias is MailSo\Imap\SearchCriterias.
type SearchCriterias struct {
	criterias []string
	fuzzy     bool
}

// Prepend is SearchCriterias::prepend().
func (s *SearchCriterias) Prepend(rule string) {
	s.criterias = append([]string{rule}, s.criterias...)
}

// String is SearchCriterias::__toString().
func (s *SearchCriterias) String() string {
	if s.fuzzy {
		keys := map[string]struct{}{"BCC": {}, "BODY": {}, "CC": {}, "FROM": {}, "SUBJECT": {}, "TEXT": {}, "TO": {}}
		for index, criterion := range s.criterias {
			if _, ok := keys[criterion]; ok {
				s.criterias[index] = "FUZZY " + criterion
			}
		}
	}
	result := strings.TrimSpace(strings.Join(s.criterias, " "))
	if "" == result {
		return "ALL"
	}
	return result
}

// searchMap is an ordered map, so fromString() iterates the parsed search
// parameters in the order they appeared (like the PHP array).
type searchMap struct {
	keys   []string
	values map[string]any
}

// newSearchMap returns an empty ordered map.
func newSearchMap() *searchMap {
	return &searchMap{values: map[string]any{}}
}

// set stores a value, keeping the first insertion position.
func (m *searchMap) set(key string, value any) {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// get returns the value and whether it is present.
func (m *searchMap) get(key string) (any, bool) {
	value, ok := m.values[key]
	return value, ok
}

// del removes a key.
func (m *searchMap) del(key string) {
	if _, ok := m.values[key]; !ok {
		return
	}
	delete(m.values, key)
	for index, key2 := range m.keys {
		if key2 == key {
			m.keys = append(m.keys[:index], m.keys[index+1:]...)
			break
		}
	}
}

// len returns the number of entries.
func (m *searchMap) len() int { return len(m.keys) }

// iterate calls fn for every entry in insertion order.
func (m *searchMap) iterate(fn func(key string, value any)) {
	for _, key := range m.keys {
		fn(key, m.values[key])
	}
}

// SearchCriteriasFromString is SearchCriterias::fromString(). It returns the
// criterias and the (possibly changed) useCache flag.
func SearchCriteriasFromString(client *imapclient.Client, folderName, search string, hideDeleted bool, useCache bool) (*SearchCriterias, bool) {
	timeFilter := 0
	var result []string

	search = strings.TrimSpace(search)
	if "" != search {
		var lines *searchMap
		if matchSearchCriteriaPrefix(search) {
			lines = parseSearchString(search)
		} else {
			lines = parseQueryString(search)
		}

		if nil == lines || 0 == lines.len() {
			value := escapeSearchString(client, search)
			if nil != client.Settings && client.Settings.FastSimpleSearch {
				result = append(result, "OR OR OR", "FROM", value, "TO", value, "CC", value, "SUBJECT", value)
			} else {
				result = append(result, "TEXT", value)
			}
		} else {
			if in, ok := lines.get("IN"); ok && client.HasCapability("MULTISEARCH") {
				if text, _ := in.(string); "subtree" == text || "subtree-one" == text || "mailboxes" == text {
					result = append(result, "IN ("+text+" "+escapeSearchString(client, folderName)+")")
				}
			}

			if email, ok := lines.get("EMAIL"); ok {
				value := escapeSearchString(client, stringOf(email))
				result = append(result, "OR OR", "FROM", value, "TO", value, "CC", value)
				lines.del("EMAIL")
				lines.del("FROM")
				lines.del("TO")
			}

			lines.iterate(func(name string, raw any) {
				if text, ok := raw.(string); ok && "" == strings.TrimSpace(text) {
					return
				}
				switch name {
				case "FROM", "SUBJECT", "BODY":
					result = append(result, name, escapeSearchString(client, stringOf(raw)))
				case "KEYWORD":
					result = append(result, name, escapeSearchString(client, imapclient.UTF8ToMUTF7(stringOf(raw))))
				case "TO":
					value := escapeSearchString(client, stringOf(raw))
					result = append(result, "OR", "TO", value, "CC", value)
				case "ATTACHMENT":
					result = append(result, "OR OR OR",
						"HEADER Content-Type application/",
						"HEADER Content-Type multipart/m",
						"HEADER Content-Type multipart/signed",
						"HEADER Content-Type multipart/report")
				case "HEADER":
					values := strings.SplitN(stringOf(raw), " ", 2)
					field := ""
					if 0 < len(values) {
						field = values[0]
					}
					if "" != strings.TrimSpace(field) {
						rest := ""
						if 1 < len(values) {
							rest = values[1]
						}
						result = append(result, "HEADER "+escapeSearchString(client, field)+" "+escapeSearchString(client, rest))
					}
				case "FLAGGED", "UNFLAGGED", "SEEN", "UNSEEN", "ANSWERED", "UNANSWERED", "DELETED", "UNDELETED":
					result = append(result, name)
					useCache = false
				case "LARGER", "SMALLER":
					result = append(result, name, strconv.Itoa(parseFriendlySize(stringOf(raw))))
				case "SINCE":
					if stamp := parseSearchDate(stringOf(raw)); 0 != stamp {
						if stamp > timeFilter {
							timeFilter = stamp
						}
					}
				case "ON", "SENTON", "SENTSINCE", "SENTBEFORE", "BEFORE":
					if stamp := parseSearchDate(stringOf(raw)); 0 != stamp {
						result = append(result, name, gmdate(stamp))
					}
				case "DATE":
					dateFrom, dateTo := 0, 0
					dates := strings.Split(stringOf(raw), "/")
					if 2 == len(dates) {
						if "" != dates[0] {
							dateFrom = parseSearchDate(dates[0])
						}
						if "" != dates[1] {
							dateTo = parseSearchDate(dates[1]) + 60*60*24
						}
					} else if "" != stringOf(raw) {
						dateFrom = parseSearchDate(stringOf(raw))
						dateTo = dateFrom + 60*60*24
					}
					if 0 < dateFrom && dateFrom > timeFilter {
						timeFilter = dateFrom
					}
					if 0 < dateTo {
						result = append(result, "BEFORE", gmdate(dateTo))
					}
				case "OLDER", "YOUNGER":
					seconds, _ := strconv.Atoi(strings.TrimSpace(stringOf(raw)))
					if 0 < seconds && client.HasCapability("WITHIN") {
						result = append(result, name, strconv.Itoa(seconds))
					}
				case "READ", "UNREAD":
					result = append(result, strings.Replace(name, "READ", "SEEN", 1))
					useCache = false
				case "OLDER_THAN":
					if target, ok := parseDateInterval(stringOf(raw)); ok {
						if client.HasCapability("WITHIN") {
							result = append(result, "OLDER", strconv.Itoa(int(time.Since(target).Seconds())))
						} else {
							result = append(result, "BEFORE", target.UTC().Format("2-Jan-2006"))
						}
					}
				case "NEWER_THAN":
					if target, ok := parseDateInterval(stringOf(raw)); ok {
						if client.HasCapability("WITHIN") {
							result = append(result, "YOUNGER", strconv.Itoa(int(time.Since(target).Seconds())))
						} else if t := int(target.Unix()); t > timeFilter {
							timeFilter = t
						}
					}
				}
			})
		}
	}

	if 0 < timeFilter {
		result = append(result, "SINCE", gmdate(timeFilter))
	}

	if hideDeleted && !containsString(result, "DELETED") && !containsString(result, "UNDELETED") {
		result = append(result, "UNDELETED")
	}

	if nil != client.Settings && "" != client.Settings.SearchFilter {
		result = append(result, client.Settings.SearchFilter)
	}

	return &SearchCriterias{criterias: result}, useCache
}

// matchSearchCriteriaPrefix reports "/^(RegEx):/i".
var searchCriteriaPrefix = mustRegexp(`(?i)^(` + searchCriteriaRegex + `):`)

// matchSearchCriteriaPrefix reports whether the search starts with a criterion.
func matchSearchCriteriaPrefix(search string) bool {
	return searchCriteriaPrefix.MatchString(search)
}

// EscapeSearchString is SearchCriterias::escapeSearchString().
func escapeSearchString(client *imapclient.Client, search string) string {
	if mime.IsASCII(search) || !client.HasCapability("LITERAL+") {
		return client.EscapeString(search)
	}
	return "{" + strconv.Itoa(len(search)) + "}\r\n" + search
}

// parseSearchDate is SearchCriterias::parseSearchDate().
func parseSearchDate(value string) int {
	value = strings.TrimSpace(value)
	if "" == value {
		return 0
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
	if nil != err {
		return 0
	}
	return int(parsed.Unix())
}

// parseFriendlySize is SearchCriterias::parseFriendlySize().
func parseFriendlySize(size string) int {
	cleaned := mustRegexp(`[^0-9KM]`).ReplaceAllString(strings.ToUpper(size), "")
	result := 0
	for index := 0; index < len(cleaned); index++ {
		character := cleaned[index]
		if '0' > character || '9' < character {
			break
		}
		result = result*10 + int(character-'0')
	}
	switch {
	case strings.HasSuffix(cleaned, "M"):
		result *= 1024
		fallthrough
	case strings.HasSuffix(cleaned, "K"):
		result *= 1024
	}
	return result
}

// parseDateInterval converts an older_than/newer_than duration to a target time
// (now minus the interval). The "ok" result is false for a malformed value.
func parseDateInterval(value string) (time.Time, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if "" == value {
		return time.Time{}, false
	}
	if !mustRegexp(`^(?:\d+[YMWD]|P(?:\d+[YMWD])*(?:T(?:\d+[HMS])+)?)$`).MatchString(value) {
		return time.Time{}, false
	}
	if 'P' != value[0] {
		value = "P" + value
	}
	duration, ok := parseISODuration(value)
	if !ok {
		return time.Time{}, false
	}
	return time.Now().Add(-duration), true
}

// parseISODuration parses a PnYnMnDTnHnMnS duration into a time.Duration.
func parseISODuration(value string) (time.Duration, bool) {
	if "" == value || 'P' != value[0] {
		return 0, false
	}
	inTime := false
	var total time.Duration
	number := 0
	haveNumber := false
	for index := 1; index < len(value); index++ {
		character := value[index]
		switch {
		case 'T' == character:
			inTime = true
		case '0' <= character && '9' >= character:
			number = number*10 + int(character-'0')
			haveNumber = true
		default:
			if !haveNumber {
				return 0, false
			}
			switch character {
			case 'Y':
				total += time.Duration(number) * 365 * 24 * time.Hour
			case 'M':
				if inTime {
					total += time.Duration(number) * time.Minute
				} else {
					total += time.Duration(number) * 30 * 24 * time.Hour
				}
			case 'W':
				total += time.Duration(number) * 7 * 24 * time.Hour
			case 'D':
				total += time.Duration(number) * 24 * time.Hour
			case 'H':
				total += time.Duration(number) * time.Hour
			case 'S':
				total += time.Duration(number) * time.Second
			default:
				return 0, false
			}
			number = 0
			haveNumber = false
		}
	}
	return total, true
}

// gmdate is PHP's gmdate('j-M-Y', $timestamp).
func gmdate(timestamp int) string {
	return time.Unix(int64(timestamp), 0).UTC().Format("2-Jan-2006")
}

// containsString reports whether values contains needle.
func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// stringOf returns the string form of a search value.
func stringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// parseQueryString is SearchCriterias::parseQueryString().
func parseQueryString(search string) *searchMap {
	result := newSearchMap()
	params := parseQuery(search)

	for _, key := range params.keys {
		value := params.values[key]
		var text string
		if list, ok := value.([]string); ok {
			text = strings.Join(list, ",")
		} else {
			text, _ = value.(string)
		}

		name := strings.ToUpper(key)
		switch name {
		case "MAIL":
			name = "EMAIL"
		case "TEXT":
			name = "BODY"
		case "SIZE", "BIGGER", "MINSIZE":
			name = "LARGER"
		case "MAXSIZE":
			name = "SMALLER"
		}

		switch name {
		case "DATE":
			text = strings.TrimRight(text, "/") + "/"
			if "" != text && text != "/" {
				result.set(name, text)
			}
		case "BODY", "EMAIL", "FROM", "TO", "SUBJECT", "KEYWORD", "IN", "SMALLER", "LARGER",
			"SINCE", "ON", "SENTON", "SENTSINCE", "SENTBEFORE", "BEFORE", "OLDER", "YOUNGER", "HEADER":
			if 0 < len(text) {
				result.set(name, text)
			}
		case "ATTACHMENT", "FLAGGED", "UNFLAGGED", "SEEN", "UNSEEN", "ANSWERED", "UNANSWERED", "DELETED", "UNDELETED":
			result.set(name, true)
		}
	}

	return result
}

// parseQuery is the parse_str() part of the query search: it url-decodes the
// keys/values and collects "key[]=" repeats into a list.
func parseQuery(search string) *searchMap {
	result := newSearchMap()
	for _, pair := range strings.Split(search, "&") {
		if "" == pair {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		key := urlUnescape(parts[0])
		value := ""
		if 2 == len(parts) {
			value = urlUnescape(parts[1])
		}

		array := false
		if strings.HasSuffix(key, "[]") {
			key = key[:len(key)-2]
			array = true
		}

		if array {
			existing, _ := result.get(key)
			list, _ := existing.([]string)
			result.set(key, append(append([]string(nil), list...), value))
		} else {
			result.set(key, value)
		}
	}
	return result
}

// urlUnescape is PHP's urldecode().
func urlUnescape(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		if '+' == character {
			builder.WriteByte(' ')
			continue
		}
		if '%' == character && index+2 < len(value) {
			if high, err := strconv.ParseUint(value[index+1:index+3], 16, 8); nil == err {
				builder.WriteByte(byte(high))
				index += 2
				continue
			}
		}
		builder.WriteByte(character)
	}
	return builder.String()
}

// parseSearchString is SearchCriterias::parseSearchString().
func parseSearchString(search string) *searchMap {
	result := newSearchMap()

	search = mime.Trim(search)
	search = strings.TrimSpace(mustRegexp(`(?i)(`+searchCriteriaRegex+`):\s+`).ReplaceAllString(search, "$1:"))

	cache := map[string]string{}
	placeholderIndex := 0
	for _, quote := range []byte{'"', '\''} {
		var modified strings.Builder
		index := 0
		for index < len(search) {
			if search[index] != quote {
				modified.WriteByte(search[index])
				index++
				continue
			}
			end := index + 1
			for end < len(search) && search[end] != quote {
				if '\\' == search[end] && end+1 < len(search) {
					end++
				}
				end++
			}
			if end >= len(search) {
				modified.WriteString(search[index:])
				break
			}
			item := search[index : end+1]
			placeholderIndex++
			key := "\x00cache" + strconv.Itoa(placeholderIndex) + "\x00"
			cache[key] = item
			modified.WriteString(key)
			index = end + 1
		}
		search = modified.String()
	}

	tokenRegex := mustRegexp(`(?i)(` + searchCriteriaRegex + `):([^\s]*)`)
	matches := tokenRegex.FindAllStringSubmatch(search, -1)
	if 0 < len(matches) {
		for _, match := range matches {
			search = strings.ReplaceAll(search, match[0], "")
		}
		search = mime.Trim(search)
	}

	for _, match := range matches {
		if 3 > len(match) || "" == match[2] {
			continue
		}
		name := strings.ToUpper(match[1])
		switch name {
		case "MAIL":
			name = "EMAIL"
		case "TEXT":
			name = "BODY"
		case "SIZE", "BIGGER", "MINSIZE":
			name = "LARGER"
		case "MAXSIZE":
			name = "SMALLER"
		}

		switch {
		case "HAS" == name && mustRegexp(`files?|attachments?`).MatchString(match[2]):
			result.set("ATTACHMENT", true)
		case "IS" == name:
			for _, value := range strings.Split(strings.ToUpper(match[2]), ",") {
				result.set(strings.TrimSpace(value), true)
			}
		case "DATE" == name:
			result.set(name, strings.ReplaceAll(match[2], ".", "-"))
		default:
			result.set(name, match[2])
		}
	}

	for _, key := range result.keys {
		if value, ok := result.values[key].(string); ok {
			if cached, ok := cache[value]; ok {
				result.values[key] = strings.Trim(cached, `"' `)
			}
		}
	}

	return result
}
