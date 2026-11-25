package contacts

// The jCard (RFC 7095) model of a vCard, the format SnappyMail stores in the
// "jcard" property of the address book and exchanges with the frontend.
//
// A card is a two element JSON array ["vcard", [<property>, …]] and a property
// is a four element array [name, parameters, type, value]. The value is either
// a string (FN, EMAIL, …) or, for structured fields such as N, ADR and ORG, an
// array of strings.
//
// The model keeps the property order and the raw values, so a card written by
// the PHP backend (Sabre\VObject) round-trips through Go unchanged.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// errInvalidCard is the error of a document that is not a jCard.
var errInvalidCard = errors.New("contacts: invalid jCard")

// Property is one jCard property: [name, parameters, type, value].
type Property struct {
	// Name is the lower case field name ("fn", "n", "email", …).
	Name string
	// Params is the parameters object ({"type":"HOME"}), possibly nil.
	Params map[string]any
	// Type is the value type, usually "text".
	Type string
	// Value is a string or, for structured fields, a []any of strings.
	Value any
}

// Card is a vCard as an ordered list of properties.
type Card struct {
	props []*Property
}

// NewCard returns an empty 4.0 card.
func NewCard() *Card {
	card := &Card{}
	card.props = append(card.props, &Property{
		Name:   "version",
		Params: map[string]any{},
		Type:   "text",
		Value:  "4.0",
	})
	return card
}

// parseJCard parses a jCard document: either the decoded JSON value or the
// JSON text (Sabre\VObject\Reader::readJson() accepts both).
func parseJCard(value any) (*Card, error) {
	if text, ok := value.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); nil != err {
			return nil, errInvalidCard
		}
		value = decoded
	}

	document, ok := value.([]any)
	if !ok || len(document) < 2 {
		return nil, errInvalidCard
	}

	kind, ok := document[0].(string)
	if !ok || !strings.EqualFold(kind, "vcard") {
		return nil, errInvalidCard
	}

	entries, ok := document[1].([]any)
	if !ok {
		return nil, errInvalidCard
	}

	card := &Card{}
	for _, entry := range entries {
		property, err := parseProperty(entry)
		if nil != err {
			return nil, err
		}
		card.props = append(card.props, property)
	}

	return card, nil
}

// parseProperty parses one [name, params, type, value] array.
func parseProperty(value any) (*Property, error) {
	entry, ok := value.([]any)
	if !ok || len(entry) < 4 {
		return nil, errInvalidCard
	}

	name, ok := entry[0].(string)
	if !ok || "" == name {
		return nil, errInvalidCard
	}

	property := &Property{Name: strings.ToLower(name), Type: "text"}

	if params, ok := entry[1].(map[string]any); ok {
		property.Params = params
	}
	if kind, ok := entry[2].(string); ok && "" != kind {
		property.Type = kind
	}
	property.Value = entry[3]

	return property, nil
}

// JCard serializes the card back into the jCard JSON structure
// (Sabre\VObject\Component\VCard::jsonSerialize()).
func (c *Card) JCard() []any {
	entries := make([]any, 0, len(c.props))
	for _, property := range c.props {
		params := property.Params
		if nil == params {
			params = map[string]any{}
		}
		entries = append(entries, []any{property.Name, params, property.Type, property.Value})
	}
	return []any{"vcard", entries}
}

// jsonText encodes the card as JSON text, the form stored in the "jcard"
// property (PHP: json_encode($oVCard)).
func (c *Card) jsonText() (string, error) {
	encoded, err := encodeJSON(c.JCard())
	if nil != err {
		return "", err
	}
	return string(encoded), nil
}

// first returns the first property of the given name.
func (c *Card) first(name string) *Property {
	name = strings.ToLower(name)
	for _, property := range c.props {
		if property.Name == name {
			return property
		}
	}
	return nil
}

// all returns every property of the given name, in order.
func (c *Card) all(name string) []*Property {
	name = strings.ToLower(name)
	result := make([]*Property, 0, 1)
	for _, property := range c.props {
		if property.Name == name {
			result = append(result, property)
		}
	}
	return result
}

// stringValue returns the string value of a property, or "" for a structured
// value.
func stringValue(property *Property) string {
	if nil == property {
		return ""
	}
	if text, ok := property.Value.(string); ok {
		return text
	}
	return ""
}

// stringList returns the value of a structured property as a list of strings.
func stringList(property *Property) []string {
	if nil == property {
		return nil
	}
	switch typed := property.Value.(type) {
	case []any:
		result := make([]string, len(typed))
		for index, item := range typed {
			if text, ok := item.(string); ok {
				result[index] = text
			}
		}
		return result
	case []string:
		return typed
	case string:
		return []string{typed}
	}
	return nil
}

// FN is the "fn" formatted name, or "".
func (c *Card) FN() string { return stringValue(c.first("fn")) }

// UID is the "uid" value, or "".
func (c *Card) UID() string { return stringValue(c.first("uid")) }

// Nickname is the "nickname" value, or "".
func (c *Card) Nickname() string { return stringValue(c.first("nickname")) }

// Note is the "note" value, or "".
func (c *Card) Note() string { return stringValue(c.first("note")) }

// NParts returns the five N components (last, first, middle, prefix, suffix),
// padded to length five.
func (c *Card) NParts() []string {
	parts := stringList(c.first("n"))
	result := make([]string, 5)
	copy(result, parts)
	return result
}

// values returns the string values of every property of the given name.
func (c *Card) values(name string) []string {
	properties := c.all(name)
	result := make([]string, 0, len(properties))
	for _, property := range properties {
		result = append(result, stringValue(property))
	}
	return result
}

// setString replaces every property of the name with a single text property.
func (c *Card) setString(name, value string) {
	c.remove(name)
	c.props = append(c.props, &Property{
		Name:   strings.ToLower(name),
		Params: map[string]any{},
		Type:   "text",
		Value:  value,
	})
}

// setStructured replaces every property of the name with a single structured
// (array) property.
func (c *Card) setStructured(name string, values []string) {
	c.remove(name)
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	c.props = append(c.props, &Property{
		Name:   strings.ToLower(name),
		Params: map[string]any{},
		Type:   "text",
		Value:  items,
	})
}

// addString appends a text property with optional parameters.
func (c *Card) addString(name, value string, params map[string]any) {
	if nil == params {
		params = map[string]any{}
	}
	c.props = append(c.props, &Property{
		Name:   strings.ToLower(name),
		Params: params,
		Type:   "text",
		Value:  value,
	})
}

// remove drops every property of the given name.
func (c *Card) remove(name string) {
	name = strings.ToLower(name)
	kept := c.props[:0]
	for _, property := range c.props {
		if property.Name != name {
			kept = append(kept, property)
		}
	}
	c.props = kept
}

// ensureUID generates a "uid" property when the card has none (Contact::setVCard()).
func (c *Card) ensureUID() {
	if "" != c.UID() {
		return
	}
	c.setString("uid", newUUID())
}

// setRev stamps REV with the given vCard date, replacing an existing value.
func (c *Card) setRev(vcardDate string) { c.setString("rev", vcardDate) }

// setProdid stamps PRODID, replacing an existing value.
func (c *Card) setProdid(prodid string) { c.setString("prodid", prodid) }

// ensureXCrypto adds the KDE/SnappyMail X-CRYPTO property Contact::setVCard()
// inserts when it is missing.
func (c *Card) ensureXCrypto() {
	if nil != c.first("x-crypto") {
		return
	}
	c.addString("x-crypto", "", map[string]any{
		"allowed":     "PGP/INLINE,PGP/MIME,S/MIME,S/MIMEOpaque",
		"signpref":    "Ask",
		"encryptpref": "Ask",
	})
}

// ensureVersion makes sure the card reports vCard 4.0.
func (c *Card) ensureVersion() {
	if "" == stringValue(c.first("version")) {
		c.props = append([]*Property{{
			Name:   "version",
			Params: map[string]any{},
			Type:   "text",
			Value:  "4.0",
		}}, c.props...)
	}
}

// byType returns the first property of name whose TYPE parameter contains the
// given type (case-insensitive), the "getByType()" lookup of the CSV export.
func (c *Card) byType(name, kind string) string {
	kind = strings.ToLower(kind)
	for _, property := range c.all(name) {
		for _, candidate := range paramValues(property.Params, "type") {
			if strings.EqualFold(candidate, kind) {
				return stringValue(property)
			}
		}
	}
	return ""
}

// paramValues returns the string values of a parameter, flattened.
func paramValues(params map[string]any, name string) []string {
	if nil == params {
		return nil
	}
	value, ok := params[name]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	case []string:
		return typed
	}
	return nil
}

// typeString is implode(',', TYPE parameters), the "prop_type_str" of a row.
func typeString(property *Property) string {
	return strings.Join(paramValues(property.Params, "type"), ",")
}

// encodeJSON is Utils::jsonEncode(): no HTML escaping, UTF-8 output.
func encodeJSON(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); nil != err {
		return nil, err
	}
	return []byte(strings.TrimRight(builder.String(), "\n")), nil
}

// newUUID is SnappyMail\UUID::generate(): a random v4 UUID.
func newUUID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); nil != err {
		return "00000000-0000-4000-8000-000000000000"
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40 // version 4
	buffer[8] = (buffer[8] & 0x3f) | 0x80 // variant 10

	encoded := hex.EncodeToString(buffer)
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32])
}
