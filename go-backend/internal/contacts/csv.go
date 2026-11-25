package contacts

// CSV export/import of the address book (Utils::VCardToCsv() and
// Utils::CsvStreamToContacts()) plus the vCard date helper shared by the store.

import (
	"bytes"
	"encoding/csv"
	"strings"
	"time"
)

// csvHeader is the fixed header row of Utils::VCardToCsv().
var csvHeader = []string{
	"Title", "First Name", "Middle Name", "Last Name", "Nick Name", "Display Name",
	"Company", "Department", "Job Title", "Office Location",
	"E-mail Address", "Notes", "Web Page", "Birthday", "Mobile Phone",
	"Home Email", "Home Phone",
	"Home Street", "Home City", "Home State", "Home Postal Code", "Home Country",
	"Business Email", "Business Phone",
	"Business Street", "Business City", "Business State", "Business Postal Code", "Business Country",
}

// vcardTimestamp formats a time as the vCard DATE-TIME "Ymd\THis\Z" of
// gmdate('Ymd\THis\Z').
func vcardTimestamp(moment time.Time) string {
	return moment.UTC().Format("20060102T150405Z")
}

// nullableADR returns the seven ADR components of the first ADR property with a
// given TYPE, or an empty set.
func nullableADR(card *Card, kind string) []string {
	empty := []string{"", "", "", "", "", "", ""}
	kind = strings.ToLower(kind)
	for _, property := range card.all("adr") {
		for _, candidate := range paramValues(property.Params, "type") {
			if strings.EqualFold(candidate, kind) {
				parts := stringList(property)
				result := make([]string, 7)
				copy(result, parts)
				return result
			}
		}
	}
	return empty
}

// firstValue is "(string) $oVCard->NAME".
func firstValue(card *Card, name string) string {
	return stringValue(card.first(name))
}

// orgFirst is "(string) $oVCard->ORG": the first ORG component.
func orgFirst(card *Card) string {
	parts := stringList(card.first("org"))
	if 0 == len(parts) {
		return ""
	}
	return parts[0]
}

// vcardToCSVRow builds the CSV row of one contact, in the column order of
// Utils::VCardToCsv().
func vcardToCSVRow(card *Card) []string {
	name := card.NParts() // last, first, middle, prefix, suffix

	home := nullableADR(card, "HOME")
	work := nullableADR(card, "WORK")

	return []string{
		card.FN(),           // Title
		name[1],             // First Name
		name[2],             // Middle Name
		name[0],             // Last Name
		card.Nickname(),     // Nick Name
		card.FN(),           // Display Name
		orgFirst(card),      // Company
		"",                  // Department
		"",                  // Job Title
		"",                  // Office Location
		firstValue(card, "email"), // E-mail Address
		card.Note(),               // Notes
		firstValue(card, "url"),   // Web Page
		firstValue(card, "bday"),  // Birthday
		card.byType("tel", "CELL"),   // Mobile Phone
		card.byType("email", "HOME"), // Home Email
		card.byType("tel", "HOME"),   // Home Phone
		strings.TrimSpace(home[1] + "\n" + home[2]), // Home Street (ext + street)
		home[3], // Home City
		home[4], // Home State
		home[5], // Home Postal Code
		home[6], // Home Country
		card.byType("email", "WORK"), // Business Email
		card.byType("tel", "WORK"),   // Business Phone
		strings.TrimSpace(work[1] + "\n" + work[2]), // Business Street
		work[3], // Business City
		work[4], // Business State
		work[5], // Business Postal Code
		work[6], // Business Country
	}
}

// csvField is the target of a CSV header column.
type csvField struct {
	Name    string // vCard property name
	NIndex  int    // N component index (-1 when not an N part)
	TypeVal string // the TYPE parameter ("" for none)
}

// csvHeaderField maps a CSV header cell onto a card field, following
// Utils::$aMap. The empty result means "ignore the column".
func csvHeaderField(header string) (csvField, bool) {
	normalised := strings.ToUpper(strings.TrimSpace(header))
	normalised = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(normalised)

	// A column named after a vCard property imports directly.
	switch normalised {
	case "FN":
		return csvField{Name: "fn", NIndex: -1}, true
	case "NICKNAME":
		return csvField{Name: "nickname", NIndex: -1}, true
	case "NOTE":
		return csvField{Name: "note", NIndex: -1}, true
	case "BDAY":
		return csvField{Name: "bday", NIndex: -1}, true
	case "ORG":
		return csvField{Name: "org", NIndex: -1}, true
	case "EMAIL":
		return csvField{Name: "email", NIndex: -1}, true
	case "TEL":
		return csvField{Name: "tel", NIndex: -1}, true
	case "URL":
		return csvField{Name: "url", NIndex: -1}, true
	}

	switch strings.ToLower(normalised) {
	// Name and title.
	case "title", "name", "fullname", "displayname":
		return csvField{Name: "fn", NIndex: -1}, true
	// Structured N parts.
	case "last", "lastname", "surname":
		return csvField{Name: "n", NIndex: 0}, true
	case "givenname", "first", "firstname":
		return csvField{Name: "n", NIndex: 1}, true
	case "middle", "middlename":
		return csvField{Name: "n", NIndex: 2}, true
	case "prefix", "nameprefix":
		return csvField{Name: "n", NIndex: 3}, true
	case "suffix", "namesuffix":
		return csvField{Name: "n", NIndex: 4}, true
	// Nickname and birthday.
	case "shortname", "nickname":
		return csvField{Name: "nickname", NIndex: -1}, true
	case "birthday", "bday":
		return csvField{Name: "bday", NIndex: -1}, true
	// Notes.
	case "notes", "note":
		return csvField{Name: "note", NIndex: -1}, true
	// Telephones.
	case "mobile", "mobilephone", "businessmobile", "businessmobilephone":
		return csvField{Name: "tel", NIndex: -1, TypeVal: "CELL"}, true
	case "homephone", "homephone2", "homephone3", "primaryphone":
		return csvField{Name: "tel", NIndex: -1, TypeVal: "HOME"}, true
	case "businessphone", "businessphone2", "businessphone3", "companyphone", "companymainphone":
		return csvField{Name: "tel", NIndex: -1, TypeVal: "WORK"}, true
	case "otherphone":
		return csvField{Name: "tel", NIndex: -1}, true
	// Web pages.
	case "web", "webpage", "website":
		return csvField{Name: "url", NIndex: -1}, true
	case "personalwebsite":
		return csvField{Name: "url", NIndex: -1, TypeVal: "HOME"}, true
	case "businessweb", "businesswebpage", "businesswebsite":
		return csvField{Name: "url", NIndex: -1, TypeVal: "WORK"}, true
	// E-mail addresses.
	case "email", "emailaddress", "homeemail", "homeemail2", "homeemail3",
		"personalemail", "personalemail2", "personalemail3",
		"primaryemail", "primaryemail2", "primaryemail3",
		"email2", "email3", "email2address", "email3address":
		return csvField{Name: "email", NIndex: -1, TypeVal: "HOME"}, true
	case "businessemail", "businessemail2", "businessemail3":
		return csvField{Name: "email", NIndex: -1, TypeVal: "WORK"}, true
	case "otheremail":
		return csvField{Name: "email", NIndex: -1}, true
	}

	return csvField{}, false
}

// ParseCSVContacts is Utils::CsvStreamToContacts(): the cards of a CSV
// document, using the delimiter detected from the header line.
func ParseCSVContacts(data []byte, header string) []*Card {
	delimiter := ','
	if strings.Count(header, ";") > strings.Count(header, ",") {
		delimiter = ';'
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	rows, err := reader.ReadAll()
	if nil != err || 0 == len(rows) {
		return nil
	}

	headers := rows[0]
	fields := make([]csvField, len(headers))
	valid := make([]bool, len(headers))
	for index, header := range headers {
		fields[index], valid[index] = csvHeaderField(header)
	}

	cards := make([]*Card, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) < 2 {
			continue
		}
		card := NewCard()
		name := make([]string, 5)
		hasName := false
		filled := false

		for index, cell := range row {
			if index >= len(fields) || !valid[index] {
				continue
			}
			cell = strings.TrimSpace(cell)
			if "" == cell {
				continue
			}
			field := fields[index]
			filled = true

			switch {
			case "n" == field.Name:
				if 0 <= field.NIndex && field.NIndex < len(name) {
					name[field.NIndex] = cell
					hasName = true
				}
			case "org" == field.Name:
				card.setStructured("org", []string{cell})
			case "" != field.TypeVal:
				card.addString(field.Name, cell, map[string]any{"type": []any{field.TypeVal}})
			default:
				card.addString(field.Name, cell, nil)
			}
		}

		if !filled {
			continue
		}
		if hasName {
			card.setStructured("n", name)
		}
		if "" == card.FN() {
			card.setString("fn", strings.TrimSpace(strings.Join(name, " ")))
		}
		cards = append(cards, card)
	}

	return cards
}
