package suggestions

// Tests of the Providers\Suggestions port: the de-duplication by lowercased
// e-mail address, the max(5, limit) floor and the slice to the limit.

import (
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
)

// fakeAddressBook is a Providers\AddressBook source.
type fakeAddressBook struct {
	active bool
	items  [][2]string
	last   int
}

// IsActive reports the configured state.
func (f *fakeAddressBook) IsActive(*account.Account) bool { return f.active }

// GetSuggestions returns the configured items and records the limit.
func (f *fakeAddressBook) GetSuggestions(_ *account.Account, _ string, limit int) [][2]string {
	f.last = limit
	return f.items
}

// fakeDriver is a plugin suggestion source.
type fakeDriver struct {
	items [][2]string
}

// Process returns the configured items.
func (d fakeDriver) Process(*account.Account, string, int) [][2]string { return d.items }

// TestProcessEmptyQuery checks that an empty query yields an empty list.
func TestProcessEmptyQuery(t *testing.T) {
	book := &fakeAddressBook{active: true, items: [][2]string{{"a@example.com", "A"}}}
	provider := New(book)

	if result := provider.Process(nil, "", 20); 0 != len(result) {
		t.Errorf("empty query result = %v, want empty", result)
	}
	if 0 != book.last {
		t.Errorf("GetSuggestions called for an empty query")
	}
}

// TestProcessDeduplicatesCaseInsensitive checks the mb_strtolower() uniqueness.
func TestProcessDeduplicatesCaseInsensitive(t *testing.T) {
	book := &fakeAddressBook{active: true, items: [][2]string{
		{"User@Example.com", "User"},
		{"user@example.com", "Dup"},
		{"other@example.com", "Other"},
	}}
	provider := New(book)

	result := provider.Process(nil, "u", 20)
	if 2 != len(result) {
		t.Fatalf("result length = %d, want 2 (%v)", len(result), result)
	}

	first, _ := result[0].([]any)
	if "User@Example.com" != first[0] {
		t.Errorf("first email = %v, want User@Example.com", first[0])
	}
}

// TestProcessMinLimit checks the max(5, limit) floor of Suggestions::Process().
func TestProcessMinLimit(t *testing.T) {
	book := &fakeAddressBook{active: true}
	provider := New(book)

	provider.Process(nil, "x", 2)
	if 5 != book.last {
		t.Errorf("GetSuggestions limit = %d, want the 5 floor", book.last)
	}
}

// TestProcessSlicesAndDrivers checks the array_slice() and the driver loop.
func TestProcessSlicesAndDrivers(t *testing.T) {
	book := &fakeAddressBook{active: true, items: [][2]string{
		{"a@example.com", "A"},
		{"b@example.com", "B"},
		{"c@example.com", "C"},
	}}
	driver := fakeDriver{items: [][2]string{
		{"a@example.com", "dup"},
		{"d@example.com", "D"},
	}}

	provider := New(book, driver)
	result := provider.Process(nil, "x", 5)
	// a, b, c, d -> driver "a" is a duplicate, "d" is kept, then the slice to 5.
	if 4 != len(result) {
		t.Fatalf("result length = %d, want 4 (%v)", len(result), result)
	}

	last, _ := result[3].([]any)
	if "d@example.com" != last[0] {
		t.Errorf("driver suggestion not appended: %v", result)
	}
}

// TestProcessInactiveAddressBook checks that an inactive address book is not
// queried.
func TestProcessInactiveAddressBook(t *testing.T) {
	book := &fakeAddressBook{active: false, items: [][2]string{{"a@example.com", "A"}}}
	provider := New(book)

	if result := provider.Process(nil, "x", 20); 0 != len(result) {
		t.Errorf("inactive address book result = %v, want empty", result)
	}
}
