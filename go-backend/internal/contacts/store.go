package contacts

// The sqlite address book of RainLoop\Providers\AddressBook\PdoAddressBook.
//
// The on-disk schema is a compatibility contract: a PHP backend may read the
// same AddressBook.sqlite at any time, so the tables, columns and the
// "jcard" property (prop_type 251) are reproduced exactly. Every read/write
// goes through database/sql with the pure-Go modernc.org/sqlite driver.

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// The PropertyType enumeration values the store reads and writes.
const (
	typeUID        = 9
	typeFullName   = 10
	typeFirstName  = 15
	typeLastName   = 16
	typeMiddleName = 17
	typeNickName   = 18
	typeNamePrefix = 20
	typeNameSuffix = 21
	typeEmail      = 30
	typePhone      = 31
	typeWebPage    = 32
	typeNote       = 110
	typeJCard      = 251
)

// searchFields is PdoAddressBook::$aSearchInFields.
var searchFields = []int{typeEmail, typeFirstName, typeLastName, typeNickName}

// searchFieldList is the SQL "IN (…)" list of searchFields.
var searchFieldList = "30,15,16,18"

// errNoEmail is the ValueError of getUserId() for an empty address.
var errNoEmail = errors.New("contacts: empty email")

// schemaStatements creates the PHP schema when it does not exist yet. The
// column set is the "sqlite" schema after the version 2 upgrade
// (prop_value_lower included).
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS rainloop_system (
		sys_name text NOT NULL,
		value_int integer NOT NULL DEFAULT 0
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ui_rainloop_system_sys_name ON rainloop_system (sys_name)`,
	`CREATE TABLE IF NOT EXISTS rainloop_users (
		id_user integer NOT NULL PRIMARY KEY,
		rl_email text NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS rl_email_rainloop_users_index ON rainloop_users (rl_email)`,
	`CREATE TABLE IF NOT EXISTS rainloop_ab_contacts (
		id_contact integer NOT NULL PRIMARY KEY,
		id_contact_str text NOT NULL DEFAULT '',
		id_user integer NOT NULL,
		display text NOT NULL DEFAULT '',
		changed integer NOT NULL DEFAULT 0,
		deleted integer NOT NULL DEFAULT 0,
		etag text NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS id_user_rainloop_ab_contacts_index ON rainloop_ab_contacts (id_user)`,
	`CREATE TABLE IF NOT EXISTS rainloop_ab_properties (
		id_prop integer NOT NULL PRIMARY KEY,
		id_contact integer NOT NULL,
		id_user integer NOT NULL,
		prop_type integer NOT NULL,
		prop_type_str text NOT NULL DEFAULT '',
		prop_value text NOT NULL DEFAULT '',
		prop_value_custom text NOT NULL DEFAULT '',
		prop_frec integer NOT NULL DEFAULT 0,
		prop_value_lower text NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS id_user_rainloop_ab_properties_index ON rainloop_ab_properties (id_user)`,
	`CREATE INDEX IF NOT EXISTS id_user_id_contact_rainloop_ab_properties_index ON rainloop_ab_properties (id_user, id_contact)`,
}

// Contact is RainLoop\Providers\AddressBook\Classes\Contact.
type Contact struct {
	ID           int64
	IDContactStr string
	Changed      int64
	ReadOnly     bool
	Etag         string
	Card         *Card
}

// JSON is Contact::jsonSerialize(): the {"@Object","id","readOnly","jCard"}
// document the frontend consumes.
func (c *Contact) JSON() map[string]any {
	return map[string]any{
		"@Object":  "Object/Contact",
		"id":       strconv.FormatInt(c.ID, 10),
		"readOnly": c.ReadOnly,
		"jCard":    c.Card.JCard(),
	}
}

// session is one opened address book of a user.
type session struct {
	db       *sql.DB
	userID   int64
	version  string
	now      func() time.Time
	email    string
}

// openSession opens the database, creates the schema when missing and resolves
// (or creates) the user of the given e-mail address.
func openSession(path, email, version string, now func() time.Time) (*session, error) {
	if "" == strings.TrimSpace(path) {
		return nil, errors.New("contacts: empty database path")
	}

	db, err := sql.Open("sqlite", path)
	if nil != err {
		return nil, err
	}
	// Serialise access within the process so the PHP backend and the Go
	// backend never race on the same file within one connection pool.
	db.SetMaxOpenConns(1)

	if err := ensureSchema(db); nil != err {
		db.Close()
		return nil, err
	}
	if nil == now {
		now = time.Now
	}

	s := &session{db: db, version: version, now: now, email: email}
	id, err := s.userIDFor(email)
	if nil != err {
		db.Close()
		return nil, err
	}
	s.userID = id

	return s, nil
}

// Close releases the database handle.
func (s *session) Close() error {
	if nil == s || nil == s.db {
		return nil
	}
	return s.db.Close()
}

// ensureSchema runs the idempotent CREATE statements and records the schema
// version, the dataBaseUpgrade() behaviour. A database created by an older PHP
// backend (schema version 1) gets the prop_value_lower column added, exactly
// like the version 2 ALTER of PdoSchema::getForDbType().
func ensureSchema(db *sql.DB) error {
	for _, statement := range schemaStatements {
		if _, err := db.Exec(statement); nil != err {
			return err
		}
	}

	if !hasColumn(db, "rainloop_ab_properties", "prop_value_lower") {
		if _, err := db.Exec(`ALTER TABLE rainloop_ab_properties ADD COLUMN prop_value_lower text NOT NULL DEFAULT ''`); nil != err {
			return err
		}
	}

	// Record the version so a later PHP upgrade sees an up-to-date database.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rainloop_system WHERE sys_name = 'sqlite-ab-version'`).Scan(&count); nil == err && 0 == count {
		_, _ = db.Exec(`INSERT INTO rainloop_system (sys_name, value_int) VALUES ('sqlite-ab-version', 2)`)
	}

	return nil
}

// hasColumn reports whether table has the given column (PRAGMA table_info).
func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if nil != err {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			defaultV  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultV, &primaryKey); nil != err {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

// userIDFor is getUserId(): the id of the user row, creating it when absent.
func (s *session) userIDFor(email string) (int64, error) {
	email = strings.TrimSpace(email)
	if "" == email {
		return 0, errNoEmail
	}

	var id int64
	err := s.db.QueryRow(`SELECT id_user FROM rainloop_users WHERE rl_email = ?`, email).Scan(&id)
	if nil == err {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	result, err := s.db.Exec(`INSERT INTO rainloop_users (rl_email) VALUES (?)`, email)
	if nil != err {
		return 0, err
	}
	return result.LastInsertId()
}

// propRow is one rainloop_ab_properties row before it is written.
type propRow struct {
	Type    int
	TypeStr string
	Value   string
}

// cardPropertyRows is Legacy::VCardToProperties(): the searchable properties of
// a card (FN, the N parts and every EMAIL/URL/TEL). The JCARD row is added by
// the writer.
func cardPropertyRows(card *Card) []propRow {
	rows := make([]propRow, 0, 8)

	if fn := strings.TrimSpace(card.FN()); "" != fn {
		rows = append(rows, propRow{Type: typeFullName, Value: fn})
	}

	if nil != card.first("n") {
		for index, value := range card.NParts() {
			value = strings.TrimSpace(value)
			if "" == value {
				continue
			}
			var propertyType int
			switch index {
			case 0:
				propertyType = typeLastName
			case 1:
				propertyType = typeFirstName
			case 2:
				propertyType = typeMiddleName
			case 3:
				propertyType = typeNamePrefix
			case 4:
				propertyType = typeNameSuffix
			}
			if 0 != propertyType {
				rows = append(rows, propRow{Type: propertyType, Value: value})
			}
		}
	}

	for _, property := range card.all("email") {
		if value := strings.TrimSpace(stringValue(property)); "" != value {
			rows = append(rows, propRow{Type: typeEmail, TypeStr: typeString(property), Value: value})
		}
	}
	for _, property := range card.all("url") {
		if value := strings.TrimSpace(stringValue(property)); "" != value {
			rows = append(rows, propRow{Type: typeWebPage, TypeStr: typeString(property), Value: value})
		}
	}
	for _, property := range card.all("tel") {
		if value := strings.TrimSpace(stringValue(property)); "" != value {
			rows = append(rows, propRow{Type: typePhone, TypeStr: typeString(property), Value: value})
		}
	}

	return rows
}

// saveContact is PdoAddressBook::ContactSave(): insert or update the contact
// row and rewrite its properties.
func (s *session) saveContact(contact *Contact) (int64, error) {
	if nil == contact || nil == contact.Card {
		return 0, errors.New("contacts: empty contact")
	}

	display := strings.TrimSpace(contact.Card.FN())

	jcardText, err := contact.Card.jsonText()
	if nil != err {
		return 0, err
	}

	rows := cardPropertyRows(contact.Card)
	allRows := append([]propRow{{Type: typeJCard, Value: jcardText}}, rows...)

	// Preserve the e-mail frequency of an existing contact.
	freq := map[string]int{}
	if 0 < contact.ID {
		freq, _ = s.emailFrequencies(contact.ID)
	}

	var id int64
	if 0 < contact.ID {
		id = contact.ID
		if _, err := s.db.Exec(`UPDATE rainloop_ab_contacts
			SET id_contact_str = ?, display = ?, changed = ?, etag = ?
			WHERE id_user = ? AND id_contact = ?`,
			contact.IDContactStr, display, contact.Changed, contact.Etag,
			s.userID, id); nil != err {
			return 0, err
		}
		if _, err := s.db.Exec(`DELETE FROM rainloop_ab_properties
			WHERE id_user = ? AND id_contact = ?`, s.userID, id); nil != err {
			return 0, err
		}
	} else {
		result, err := s.db.Exec(`INSERT INTO rainloop_ab_contacts
			(id_user, id_contact_str, display, changed, deleted, etag)
			VALUES (?, ?, ?, ?, 0, ?)`,
			s.userID, contact.IDContactStr, display, contact.Changed, contact.Etag)
		if nil != err {
			return 0, err
		}
		id, err = result.LastInsertId()
		if nil != err {
			return 0, err
		}
	}

	for _, row := range allRows {
		frequency := 0
		if row.Type == typeEmail {
			frequency = freq[row.Value]
		}
		if _, err := s.db.Exec(`INSERT INTO rainloop_ab_properties
			(id_contact, id_user, prop_type, prop_type_str, prop_value, prop_value_lower, prop_value_custom, prop_frec)
			VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
			id, s.userID, row.Type, row.TypeStr, row.Value, strings.ToLower(row.Value), frequency); nil != err {
			return 0, err
		}
	}

	return id, nil
}

// emailFrequencies is getContactFreq(): prop_value → prop_frec of the e-mail
// properties of a contact.
func (s *session) emailFrequencies(id int64) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT prop_value, prop_frec FROM rainloop_ab_properties
		WHERE id_user = ? AND id_contact = ? AND prop_type = ?`, s.userID, id, typeEmail)
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	result := map[string]int{}
	for rows.Next() {
		var value string
		var frequency int
		if err := rows.Scan(&value, &frequency); nil == err && "" != value {
			result[value] = frequency
		}
	}
	return result, rows.Err()
}

// deleteContacts is DeleteContacts(): soft-delete the contact rows and drop
// their properties.
func (s *session) deleteContacts(ids []int64) error {
	filtered := make([]int64, 0, len(ids))
	for _, id := range ids {
		if 0 < id {
			filtered = append(filtered, id)
		}
	}
	if 0 == len(filtered) {
		return nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filtered)), ",")
	args := make([]any, 0, len(filtered)+1)
	for _, id := range filtered {
		args = append(args, id)
	}

	if _, err := s.db.Exec(`DELETE FROM rainloop_ab_properties
		WHERE id_user = ? AND id_contact IN (`+placeholders+`)`,
		append([]any{s.userID}, args...)...); nil != err {
		return err
	}

	updateArgs := append([]any{s.now().Unix(), s.userID}, args...)
	_, err := s.db.Exec(`UPDATE rainloop_ab_contacts SET deleted = 1, changed = ?
		WHERE id_user = ? AND id_contact IN (`+placeholders+`)`, updateArgs...)
	return err
}

// getContacts is GetContacts(): the paged contacts plus the total count.
func (s *session) getContacts(offset, limit int, search string) ([]*Contact, int, error) {
	search = strings.TrimSpace(search)

	var searchIDs []int64
	count := 0

	if "" != search {
		pattern := searchPattern(search)
		rows, err := s.db.Query(`SELECT DISTINCT id_contact FROM rainloop_ab_properties
			WHERE id_user = ? AND prop_type IN (`+searchFieldList+`) AND (
				prop_value LIKE ? ESCAPE '=' OR
				(prop_value_lower <> '' AND prop_value_lower LIKE ? ESCAPE '=')
			)`, s.userID, pattern, searchPattern(strings.ToLower(search)))
		if nil != err {
			return nil, 0, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); nil == err && 0 < id {
				searchIDs = append(searchIDs, id)
			}
		}
		rows.Close()
		if err := rows.Err(); nil != err {
			return nil, 0, err
		}
		count = len(searchIDs)
	} else {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM rainloop_ab_contacts
			WHERE id_user = ? AND deleted = 0`, s.userID).Scan(&count); nil != err {
			return nil, 0, err
		}
	}

	if 0 == count {
		return []*Contact{}, 0, nil
	}

	query := `SELECT c.id_contact, c.id_contact_str, c.changed, c.etag, p.prop_value AS jcard
		FROM rainloop_ab_contacts AS c
		LEFT JOIN rainloop_ab_properties AS p ON (p.id_contact = c.id_contact AND p.prop_type = ?)
		WHERE c.deleted = 0 AND c.id_user = ?`
	args := []any{typeJCard, s.userID}

	if 0 < len(searchIDs) {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(searchIDs)), ",")
		query += ` AND c.id_contact IN (` + placeholders + `)`
		for _, id := range searchIDs {
			args = append(args, id)
		}
	}

	query += ` ORDER BY display ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if nil != err {
		return nil, 0, err
	}
	defer rows.Close()

	list, err := s.contactsFromRows(rows)
	if nil != err {
		return nil, 0, err
	}

	// count is the total number of hits (PHP passes $iResultCount by reference
	// before the paged SELECT).
	return list, count, nil
}

// contactsFromRows is getContactsFromPDO(): the jCard branch and the legacy
// property rebuild.
func (s *session) contactsFromRows(rows *sql.Rows) ([]*Contact, error) {
	list := []*Contact{}
	legacy := []int64{}

	for rows.Next() {
		var (
			id      int64
			idStr   string
			changed int64
			etag    string
			jcard   sql.NullString
		)
		if err := rows.Scan(&id, &idStr, &changed, &etag, &jcard); nil != err {
			return nil, err
		}

		contact := &Contact{ID: id, IDContactStr: idStr, Changed: changed, Etag: etag}
		if jcard.Valid && "" != strings.TrimSpace(jcard.String) {
			if card, err := parseJCard(jcard.String); nil == err {
				contact.Card = card
			} else {
				legacy = append(legacy, id)
				contact.Card = NewCard()
			}
		} else {
			legacy = append(legacy, id)
			contact.Card = NewCard()
		}
		list = append(list, contact)
	}
	if err := rows.Err(); nil != err {
		return nil, err
	}

	if 0 < len(legacy) {
		for _, contact := range list {
			if 0 == len(contact.Card.all("fn")) && 0 == len(contact.Card.all("email")) {
				if card, err := s.legacyCard(contact.ID, contact.Changed); nil == err {
					contact.Card = card
				}
			}
		}
	}

	return list, nil
}

// legacyCard rebuilds a vCard from the old RainLoop property rows
// (getContactsFromPDO()'s "missing jCard" branch).
func (s *session) legacyCard(id, changed int64) (*Card, error) {
	rows, err := s.db.Query(`SELECT prop_type, prop_type_str, prop_value FROM rainloop_ab_properties
		WHERE id_contact = ? AND prop_type <> ? ORDER BY id_prop ASC`, id, typeJCard)
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	card := NewCard()

	var uid, firstName, lastName, middleName, prefix, suffix string

	for rows.Next() {
		var (
			propertyType int
			typeStr      string
			value        string
		)
		if err := rows.Scan(&propertyType, &typeStr, &value); nil != err {
			return nil, err
		}

		switch propertyType {
		case typeFullName:
			card.setString("fn", value)
		case typeNickName:
			card.addString("nickname", value, nil)
		case typeNote:
			card.addString("note", value, nil)
		case typeUID:
			uid = value
		case typeFirstName:
			firstName = value
		case typeLastName:
			lastName = value
		case typeMiddleName:
			middleName = value
		case typeNamePrefix:
			prefix = value
		case typeNameSuffix:
			suffix = value
		case typeEmail:
			card.addString("email", value, typeParams(typeStr))
		case typeWebPage:
			card.addString("url", value, typeParams(typeStr))
		case typePhone:
			card.addString("tel", value, typeParams(typeStr))
		}
	}
	if err := rows.Err(); nil != err {
		return nil, err
	}

	if "" == uid {
		uid = newUUID()
	}
	card.setString("uid", uid)
	card.setStructured("n", []string{lastName, firstName, middleName, prefix, suffix})
	if 0 < changed {
		card.setRev(vcardTimestamp(time.Unix(changed, 0)))
	}

	return card, nil
}

// getContactByID is GetContactByID(): one contact by its numeric id.
func (s *session) getContactByID(id int64) (*Contact, error) {
	row := s.db.QueryRow(`SELECT c.id_contact, c.id_contact_str, c.changed, c.etag, p.prop_value AS jcard
		FROM rainloop_ab_contacts AS c
		LEFT JOIN rainloop_ab_properties AS p ON (p.id_contact = c.id_contact AND p.prop_type = ?)
		WHERE c.id_user = ? AND c.deleted = 0 AND c.id_contact = ? LIMIT 1`,
		typeJCard, s.userID, id)

	var (
		contactID int64
		idStr     string
		changed   int64
		etag      string
		jcard     sql.NullString
	)
	if err := row.Scan(&contactID, &idStr, &changed, &etag, &jcard); nil != err {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	contact := &Contact{ID: contactID, IDContactStr: idStr, Changed: changed, Etag: etag}
	if jcard.Valid && "" != strings.TrimSpace(jcard.String) {
		if card, err := parseJCard(jcard.String); nil == err {
			contact.Card = card
			return contact, nil
		}
	}
	card, err := s.legacyCard(contact.ID, contact.Changed)
	if nil != err {
		return nil, err
	}
	contact.Card = card
	return contact, nil
}

// exportContacts returns the non-deleted contacts ordered by id.
func (s *session) exportContacts() ([]*Contact, error) {
	rows, err := s.db.Query(`SELECT id_contact FROM rainloop_ab_contacts
		WHERE id_user = ? AND deleted = 0 ORDER BY id_contact ASC`, s.userID)
	if nil != err {
		return nil, err
	}

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); nil == err {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); nil != err {
		return nil, err
	}

	result := make([]*Contact, 0, len(ids))
	for _, id := range ids {
		contact, err := s.getContactByID(id)
		if nil != err || nil == contact {
			continue
		}
		result = append(result, contact)
	}
	return result, nil
}

// exportVCF is Export('vcf'): the serialized cards.
func (s *session) exportVCF() ([]byte, error) {
	list, err := s.exportContacts()
	if nil != err {
		return nil, err
	}
	var builder strings.Builder
	for _, contact := range list {
		builder.WriteString(contact.Card.SerializeText())
	}
	return []byte(builder.String()), nil
}

// exportCSV is Export('csv'): the header row plus one row per contact, in the
// exact column order of Utils::VCardToCsv().
func (s *session) exportCSV() ([]byte, error) {
	list, err := s.exportContacts()
	if nil != err {
		return nil, err
	}

	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	if err := writer.Write(csvHeader); nil != err {
		return nil, err
	}
	for _, contact := range list {
		if err := writer.Write(vcardToCSVRow(contact.Card)); nil != err {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); nil != err {
		return nil, err
	}
	return []byte(builder.String()), nil
}

// getSuggestions is GetSuggestions(): the [email, name] pairs of the contacts
// that match the query.
func (s *session) getSuggestions(search string, limit int) ([][2]string, error) {
	search = strings.TrimSpace(search)
	if "" == search {
		return nil, nil
	}

	pattern := searchPattern(search)
	rows, err := s.db.Query(`SELECT DISTINCT id_contact FROM rainloop_ab_properties
		WHERE id_user = ? AND prop_type IN (`+searchFieldList+`) AND (
			prop_value LIKE ? ESCAPE '=' OR
			(prop_value_lower <> '' AND prop_value_lower LIKE ? ESCAPE '=')
		)`, s.userID, pattern, searchPattern(strings.ToLower(search)))
	if nil != err {
		return nil, err
	}

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); nil == err && 0 < id {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); nil != err {
		return nil, err
	}

	result := make([][2]string, 0, len(ids))
	for _, id := range ids {
		emails, name, nick, err := s.contactEmails(id)
		if nil != err {
			continue
		}
		display := strings.TrimSpace(name)
		if "" == display {
			display = nick
		}
		for _, email := range emails {
			if "" != email {
				result = append(result, [2]string{email, display})
			}
		}
		if 0 < limit && len(result) >= limit {
			break
		}
	}

	if 0 < limit && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// contactEmails returns the e-mail values, the "first last" name and the
// nickname of one contact.
func (s *session) contactEmails(id int64) (emails []string, name, nick string, err error) {
	rows, err := s.db.Query(`SELECT prop_type, prop_value FROM rainloop_ab_properties
		WHERE id_user = ? AND id_contact = ? AND prop_type IN (30,15,16,18)
		ORDER BY id_prop ASC`, s.userID, id)
	if nil != err {
		return nil, "", "", err
	}
	defer rows.Close()

	first, last := "", ""
	for rows.Next() {
		var propertyType int
		var value string
		if err := rows.Scan(&propertyType, &value); nil != err {
			continue
		}
		switch propertyType {
		case typeEmail:
			emails = append(emails, value)
		case typeFirstName:
			first = value
		case typeLastName:
			last = value
		case typeNickName:
			nick = value
		}
	}
	return emails, strings.TrimSpace(first + " " + last), nick, rows.Err()
}

// searchPattern is specialConvertSearchValue(): wrap the escaped query in %.
func searchPattern(value string) string {
	escaped := strings.NewReplacer(
		"=", "==",
		"_", "=_",
		"%", "=%",
	).Replace(value)
	return "%" + escaped + "%"
}

// typeParams rebuilds the {"type": [...]} parameters of a property row.
func typeParams(typeStr string) map[string]any {
	typeStr = strings.ReplaceAll(typeStr, " ", "")
	if "" == typeStr {
		return nil
	}
	parts := strings.Split(typeStr, ",")
	values := make([]any, 0, len(parts))
	for _, part := range parts {
		if "" != part {
			values = append(values, part)
		}
	}
	if 0 == len(values) {
		return nil
	}
	return map[string]any{"type": values}
}
