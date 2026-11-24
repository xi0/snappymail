// Package suggestions ports RainLoop\Providers\Suggestions: the address-book
// recipient suggestions behind ?/Json&Action=Suggestions.
//
// The provider collects the hits of the address book and of every registered
// suggestion driver, keeps the first occurrence of each e-mail address
// (compared case-insensitively, like mb_strtolower()) and slices the result to
// the requested limit. The concrete sqlite address book is added in Step 22;
// this package only depends on the interface it will satisfy, so DoSuggestions
// already behaves like PHP (an empty list) while no address book is wired.
package suggestions

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
)

// minLimit is the "max(5, (int) $iLimit)" floor of Suggestions::Process().
const minLimit = 5

// AddressBook is the part of Providers\AddressBook that Suggestions::Process()
// uses: AddressBookProvider($oAccount)->IsActive() and GetSuggestions().
//
// GetSuggestions returns [email, name] pairs like the PHP method.
type AddressBook interface {
	// IsActive is Providers\AddressBook::IsActive().
	IsActive(account *account.Account) bool
	// GetSuggestions is Providers\AddressBook::GetSuggestions().
	GetSuggestions(account *account.Account, query string, limit int) [][2]string
}

// Driver is Providers\Suggestions\ISuggestions: a plugin supplied suggestion
// source.
type Driver interface {
	// Process is ISuggestions::Process().
	Process(account *account.Account, query string, limit int) [][2]string
}

// Provider is RainLoop\Providers\Suggestions.
type Provider struct {
	addressBook AddressBook
	drivers     []Driver
}

// New builds the provider from the address book and the plugin drivers, like
// the Suggestions constructor filters its driver array.
func New(addressBook AddressBook, drivers ...Driver) *Provider {
	filtered := make([]Driver, 0, len(drivers))
	for _, driver := range drivers {
		if nil != driver {
			filtered = append(filtered, driver)
		}
	}
	return &Provider{addressBook: addressBook, drivers: filtered}
}

// Process is Suggestions::Process(): the joined, de-duplicated suggestions of
// the address book and the drivers, limited to limit entries.
//
// The return value is a JSON-ready list of [email, name] pairs.
func (p *Provider) Process(resolved *account.Account, query string, limit int) []any {
	if 0 == len(query) {
		return []any{}
	}

	if minLimit > limit {
		limit = minLimit
	}

	seen := make(map[string]struct{})
	result := make([][2]string, 0, limit)

	add := func(items [][2]string) {
		for _, item := range items {
			// Unique e-mail address (mb_strtolower($aItem[0])).
			key := strings.ToLower(item[0])
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}

	// Address Book.
	if nil != p && nil != p.addressBook && p.addressBook.IsActive(resolved) {
		add(p.addressBook.GetSuggestions(resolved, query, limit))
	}

	// Extensions/Plugins.
	if nil != p {
		for _, driver := range p.drivers {
			items := driver.Process(resolved, query, limit)
			if 0 == len(items) {
				continue
			}
			add(items)
			if limit < len(result) {
				break
			}
		}
	}

	// array_slice(array_values($aResult), 0, $iLimit).
	if limit < len(result) {
		result = result[:limit]
	}

	out := make([]any, 0, len(result))
	for _, item := range result {
		out = append(out, []any{item[0], item[1]})
	}
	return out
}
