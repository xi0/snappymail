package account

// The "additionalaccounts" document of Actions\Accounts::GetAccounts()/
// SetAccounts() and the legacy-free part of the additional account handling.
//
// The PHP storage keeps a JSON *object* keyed by address whose insertion order is
// the order the accounts were added / reordered in. A Go map would lose that
// order, so Accounts keeps the key order explicitly and decodes/encodes the
// document with an ordered token stream. This is a compatibility contract: the
// PHP backend reads the same file with json_decode() and the frontend displays
// the list in that order.

import (
	"encoding/json"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// additionalAccountsKey is the CONFIG key of the additional account list.
const additionalAccountsKey = "additionalaccounts"

// Accounts is the "additionalaccounts" document: the token array of every
// additional account keyed by its (punycode) address, in insertion order.
type Accounts struct {
	order  []string
	values map[string]any
}

// NewAccounts builds an empty document.
func NewAccounts() *Accounts {
	return &Accounts{values: map[string]any{}}
}

// Order returns the addresses in document order.
func (a *Accounts) Order() []string {
	if nil == a {
		return nil
	}
	return append([]string(nil), a.order...)
}

// Get returns the token array of an address.
func (a *Accounts) Get(email string) (any, bool) {
	if nil == a {
		return nil, false
	}
	value, ok := a.values[email]
	return value, ok
}

// Has reports whether an address is present.
func (a *Accounts) Has(email string) bool {
	if nil == a {
		return false
	}
	_, ok := a.values[email]
	return ok
}

// Set stores the token array of an address; a new address is appended.
func (a *Accounts) Set(email string, value any) {
	if nil == a {
		return
	}
	if nil == a.values {
		a.values = map[string]any{}
	}
	if _, exists := a.values[email]; !exists {
		a.order = append(a.order, email)
	}
	a.values[email] = value
}

// Delete removes an address.
func (a *Accounts) Delete(email string) {
	if nil == a {
		return
	}
	if _, exists := a.values[email]; !exists {
		return
	}
	delete(a.values, email)

	order := make([]string, 0, len(a.order))
	for _, current := range a.order {
		if current != email {
			order = append(order, current)
		}
	}
	a.order = order
}

// Len is count($aAccounts).
func (a *Accounts) Len() int {
	if nil == a {
		return 0
	}
	return len(a.order)
}

// Reorder is the array_filter(array_merge(array_fill_keys($requested, null),
// $accounts)) of DoAccountsAndIdentitiesSortOrder(): the requested addresses
// first (the ones that still exist), then any remaining accounts in their old
// order.
func (a *Accounts) Reorder(requested []string) {
	if nil == a {
		return
	}

	seen := make(map[string]struct{}, len(a.order))
	order := make([]string, 0, len(a.order))

	for _, email := range requested {
		if _, duplicate := seen[email]; duplicate {
			continue
		}
		seen[email] = struct{}{}
		if _, exists := a.values[email]; exists {
			order = append(order, email)
		}
	}
	for _, email := range a.order {
		if _, already := seen[email]; already {
			continue
		}
		seen[email] = struct{}{}
		order = append(order, email)
	}

	a.order = order
}

// ToMap is the document as a plain map, for the auth.SwitchAccount() lookup and
// for callers that only read one entry by key.
func (a *Accounts) ToMap() map[string]any {
	if nil == a {
		return map[string]any{}
	}
	result := make(map[string]any, len(a.values))
	for _, email := range a.order {
		result[email] = a.values[email]
	}
	return result
}

// String is json_encode($aAccounts): the object with the keys in document order.
func (a *Accounts) String() string {
	if nil == a || 0 == len(a.order) {
		return "{}"
	}

	var builder strings.Builder
	builder.WriteByte('{')
	for index, email := range a.order {
		if 0 < index {
			builder.WriteByte(',')
		}
		key, err := json.Marshal(email)
		if nil != err {
			continue
		}
		builder.Write(key)
		builder.WriteByte(':')

		value, err := json.Marshal(a.values[email])
		if nil != err {
			builder.WriteString("null")
			continue
		}
		builder.Write(value)
	}
	builder.WriteByte('}')

	return builder.String()
}

// LoadAccounts is Actions\Accounts::GetAccounts() reading the CONFIG entry.
//
// A missing or unreadable document yields an empty list; the legacy "accounts"
// conversion of SnappyMail\Upgrade::ConvertInsecureAccounts() is not ported.
func LoadAccounts(driver Storage, main *MainAccount) *Accounts {
	if nil == driver || nil == main {
		return NewAccounts()
	}

	content, ok := driver.Get(main, storage.TypeConfig, additionalAccountsKey)
	if !ok || "" == content {
		return NewAccounts()
	}

	return decodeAccounts(content)
}

// SaveAccounts is Actions\Accounts::SetAccounts(): a non-empty list is written,
// an empty one clears the key.
func SaveAccounts(driver Storage, main *MainAccount, accounts *Accounts) error {
	if nil == driver || nil == main {
		return nil
	}

	if nil == accounts || 0 == accounts.Len() {
		driver.Clear(main, storage.TypeConfig, additionalAccountsKey)
		return nil
	}

	return driver.Put(main, storage.TypeConfig, additionalAccountsKey, accounts.String())
}

// decodeAccounts parses the document through a token stream so the key order is
// preserved (json.Unmarshal into a map would not).
func decodeAccounts(content string) *Accounts {
	accounts := NewAccounts()

	decoder := json.NewDecoder(strings.NewReader(content))

	token, err := decoder.Token()
	if nil != err {
		return accounts
	}
	if open, ok := token.(json.Delim); !ok || '{' != open {
		return accounts
	}

	for decoder.More() {
		keyToken, err := decoder.Token()
		if nil != err {
			return accounts
		}
		key, ok := keyToken.(string)
		if !ok {
			return accounts
		}

		var value any
		if err := decoder.Decode(&value); nil != err {
			return accounts
		}
		accounts.Set(key, value)
	}

	return accounts
}
