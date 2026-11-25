package contacts

// The Provider ties the sqlite store to one installation: it resolves the
// database location per account (the global AddressBook.sqlite of the private
// data folder or the per-account ROOT copy), reads the "contacts_sync" blob and
// exposes the methods the service (?/AppData, ?/Raw), suggestion provider and
// JSON dispatcher consume.
//
// It is safe to share one Provider between those consumers: it holds no request
// specific state, only the installation configuration.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
)

// SyncStore is the CONFIG "contacts_sync" entry (Providers\Storage CONFIG).
// main wires the storage provider here; a nil store disables sync data.
type SyncStore interface {
	GetSync(account *account.Account) (string, bool)
	PutSync(account *account.Account, value string) error
}

// Options is the installation level configuration of the address book.
type Options struct {
	// Version is APP_VERSION (PRODID "SnappyMail-<version>").
	Version string
	// Enabled is GetCapa(Capa::CONTACTS) (contacts.enable): without it the
	// provider reports every account as having no address book.
	Enabled bool
	// PrivateData is APP_PRIVATE_DATA (the global AddressBook.sqlite lives at
	// its root). Without a trailing separator.
	PrivateData string
	// Global selects the private data copy; otherwise the per-account ROOT
	// folder is used (contacts.sqlite_global).
	Global bool
	// RootPath is StorageProvider()->GenerateFilePath($account, ROOT) and must
	// return a path that already exists (or which the store can create).
	RootPath func(*account.Account) string
	// Sync reads/writes the contacts_sync blob.
	Sync SyncStore
	// Now is the clock of the REV stamp; nil uses time.Now.
	Now func() time.Time
}

// Provider is RainLoop\Providers\AddressBook backed by the sqlite driver.
type Provider struct {
	options Options
}

// New builds the provider.
func New(options Options) *Provider { return &Provider{options: options} }

// IsActive is Providers\AddressBook::IsActive(): the driver exists (contacts
// enabled) and this is a sqlite build.
func (p *Provider) IsActive(_ *account.Account) bool {
	return nil != p && p.options.Enabled
}

// now returns the configured clock.
func (p *Provider) now() time.Time {
	if nil != p && nil != p.options.Now {
		return p.options.Now()
	}
	return time.Now()
}

// databasePath is the sqlite DSN of PdoAddressBook::__construct(): the private
// data file for a global address book, else the account ROOT copy.
func (p *Provider) databasePath(account *account.Account) (string, bool) {
	if nil == p {
		return "", false
	}
	if !p.options.Global && nil != p.options.RootPath {
		if root := p.options.RootPath(account); "" != root {
			return filepath.Join(root, "AddressBook.sqlite"), true
		}
	}
	if "" == p.options.PrivateData {
		return "", false
	}
	return filepath.Join(p.options.PrivateData, "AddressBook.sqlite"), true
}

// open resolves the database of the account and opens a session.
func (p *Provider) open(account *account.Account) (*session, error) {
	if nil == p || !p.options.Enabled {
		return nil, errors.New("contacts: address book disabled")
	}
	path, ok := p.databasePath(account)
	if !ok {
		return nil, errors.New("contacts: no database path")
	}

	// The PHP setup creates the folder with 0700 before PDO opens the file.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
		return nil, err
	}

	email := ""
	if nil != account {
		email = account.Email()
	}
	return openSession(path, email, p.options.Version, p.options.Now)
}

// normaliseCard stamps the fields Contact::setVCard()/ContactSave() add.
func (p *Provider) normaliseCard(card *Card) {
	card.ensureVersion()
	card.setRev(vcardTimestamp(p.now()))
	card.setProdid("SnappyMail-" + p.options.Version)
	card.ensureXCrypto()
	card.ensureUID()
}

// GetContacts is Providers\AddressBook::GetContacts(): the JSON documents of a
// page of contacts plus the total count.
func (p *Provider) GetContacts(account *account.Account, offset, limit int, search string) ([]any, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}

	s, err := p.open(account)
	if nil != err {
		return []any{}, 0
	}
	defer s.Close()

	list, count, err := s.getContacts(offset, limit, search)
	if nil != err {
		return []any{}, 0
	}

	result := make([]any, 0, len(list))
	for _, contact := range list {
		result = append(result, contact.JSON())
	}
	return result, count
}

// ContactSave is Actions\Contacts::DoContactSave() + PdoAddressBook::ContactSave():
// it parses the jCard, stamps REV/PRODID/X-CRYPTO/UID, resolves the contact of
// the (numeric) uid and stores it, returning the (string) id.
func (p *Provider) ContactSave(account *account.Account, jcard any, uid string) (string, bool) {
	card, err := parseJCard(jcard)
	if nil != err {
		return "", false
	}
	p.normaliseCard(card)

	contact := &Contact{
		Card:         card,
		Changed:      p.now().Unix(),
		IDContactStr: card.UID(),
	}

	if id, ok := numericID(uid); ok && 0 < id {
		s, err := p.open(account)
		if nil == err {
			if existing, getErr := s.getContactByID(id); nil == getErr && nil != existing {
				contact.ID = existing.ID
				contact.Etag = existing.Etag
			}
			s.Close()
		}
	}

	s, err := p.open(account)
	if nil != err {
		return "", false
	}
	defer s.Close()

	id, err := s.saveContact(contact)
	if nil != err || 0 >= id {
		return "", false
	}
	return strconv.FormatInt(id, 10), true
}

// DeleteContacts is Providers\AddressBook::DeleteContacts().
func (p *Provider) DeleteContacts(account *account.Account, ids []int) bool {
	if 0 == len(ids) {
		return false
	}
	converted := make([]int64, 0, len(ids))
	for _, id := range ids {
		if 0 < id {
			converted = append(converted, int64(id))
		}
	}
	if 0 == len(converted) {
		return false
	}

	s, err := p.open(account)
	if nil != err {
		return false
	}
	defer s.Close()

	return nil == s.deleteContacts(converted)
}

// GetSuggestions is Providers\AddressBook::GetSuggestions(): the [email, name]
// pairs of the matching contacts.
func (p *Provider) GetSuggestions(account *account.Account, query string, limit int) [][2]string {
	s, err := p.open(account)
	if nil != err {
		return nil
	}
	defer s.Close()

	result, err := s.getSuggestions(query, limit)
	if nil != err {
		return nil
	}
	return result
}

// Export is Actions\Contacts::RawContactsVcf()/RawContactsCsv(): the serialized
// address book as VCF (default) or CSV.
func (p *Provider) Export(account *account.Account, kind string) ([]byte, bool) {
	s, err := p.open(account)
	if nil != err {
		return nil, false
	}
	defer s.Close()

	if "csv" == kind {
		data, err := s.exportCSV()
		if nil != err {
			return nil, false
		}
		return data, true
	}

	data, err := s.exportVCF()
	if nil != err {
		return nil, false
	}
	return data, true
}

// ImportVcfFile is Actions\Contacts::importContactsFromVcfFile().
func (p *Provider) ImportVcfFile(account *account.Account, data []byte) int {
	return p.importCards(account, ParseVCardText(data))
}

// ImportCsvFile is Actions\Contacts::importContactsFromCsvFile().
func (p *Provider) ImportCsvFile(account *account.Account, data []byte, header string) int {
	return p.importCards(account, ParseCSVContacts(data, header))
}

// importCards stores every parsed card, counting the successful saves.
func (p *Provider) importCards(account *account.Account, cards []*Card) int {
	if nil == p || !p.options.Enabled || 0 == len(cards) {
		return 0
	}

	s, err := p.open(account)
	if nil != err {
		return 0
	}
	defer s.Close()

	count := 0
	for _, card := range cards {
		p.normaliseCard(card)
		contact := &Contact{
			Card:         card,
			Changed:      p.now().Unix(),
			IDContactStr: card.UID(),
		}
		if id, err := s.saveContact(contact); nil == err && 0 < id {
			count++
		}
	}
	return count
}

// syncDocument is the JSON layout of the contacts_sync blob, ordered like the
// PHP json_encode() of setContactsSyncData().
type syncDocument struct {
	Mode         int    `json:"Mode"`
	User         string `json:"User"`
	Password     string `json:"Password"`
	Url          string `json:"Url"`
	PasswordHMAC any    `json:"PasswordHMAC"`
}

// SyncData is Actions\Contacts::getContactsSyncData(): the stored sync blob, or
// nil. The password is not escrowed through the main account crypt key (no DAV
// consumer exists in the Go backend), so it is always reported empty.
func (p *Provider) SyncData(account *account.Account) map[string]any {
	if nil == p || nil == p.options.Sync {
		return nil
	}
	raw, ok := p.options.Sync.GetSync(account)
	if !ok || "" == strings.TrimSpace(raw) {
		return nil
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(raw), &document); nil != err || nil == document {
		return nil
	}

	// A legacy blob without Mode falls back to Enable.
	if _, ok := document["Mode"]; !ok {
		if boolFromAny(document["Enable"]) {
			document["Mode"] = 1.0
		} else {
			document["Mode"] = 0.0
		}
	}
	// The password never leaves the server; AppData replaces a stored value
	// with the dummy. Without escrow there is never a stored value.
	document["Password"] = ""

	return document
}

// SaveSyncData is Actions\Contacts::DoSaveContactsSyncData(): persist the sync
// blob. The password is not escrowed (see SyncData), so it is stored blank.
func (p *Provider) SaveSyncData(account *account.Account, data map[string]any) bool {
	if nil == p || nil == p.options.Sync {
		return false
	}

	document := syncDocument{
		Mode:     phpIntFromAny(data["Mode"]),
		User:     stringFromAny(data["User"]),
		Password: "",
		Url:      stringFromAny(data["Url"]),
	}

	encoded, err := encodeJSON(document)
	if nil != err {
		return false
	}
	return nil == p.options.Sync.PutSync(account, string(encoded))
}

// numericID parses a numeric contact id (PHP: (int) $mID).
func numericID(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if "" == value {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if nil != err {
		return 0, false
	}
	return id, true
}

// stringFromAny is PHP's (string) cast for the scalars a JSON body holds.
func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}
		return ""
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return ""
}

// phpIntFromAny is PHP's (int) cast for the scalars a JSON body holds.
func phpIntFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		number, _ := strconv.Atoi(strings.TrimSpace(typed))
		return number
	}
	return 0
}
