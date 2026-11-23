package service

// Tests of Actions::Cacher($oAccount): the per-account cache client the
// read-receipt marks of DoMessage live in.

import (
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// cacheAccount builds a main account for the cacher tests.
func cacheAccount(email string) *account.Account {
	main := account.NewMainAccount()
	main.SetCredentials(nil, email, email, "secret", "", "")
	return &main.Account
}

// TestNewCacherIsPerAccount checks that two accounts get two separate folders of
// the same cache directory, both below the configured cache folder.
func TestNewCacherIsPerAccount(t *testing.T) {
	dir := t.TempDir()

	driver, err := storage.NewCacheFileDriver(dir)
	if nil != err {
		t.Fatalf("cache driver: %v", err)
	}
	system := storage.NewCache(driver)

	appData := NewAppData(Options{Cache: system})

	first := appData.NewCacher(cacheAccount("user@example.com"))
	if nil == first {
		t.Fatal("NewCacher returned nil")
	}
	second := appData.NewCacher(cacheAccount("other@example.com"))

	key := storage.ReadReceiptCacheKey("user@example.com", "INBOX", 42)
	if err := first.Set(key, "1"); nil != err {
		t.Fatalf("Set: %v", err)
	}

	if value := first.GetDefault(key, ""); "1" != value {
		t.Errorf("first cacher = %q", value)
	}
	// A different account must not see the entry.
	if value := second.GetDefault(key, ""); "" != value {
		t.Errorf("second cacher = %q", value)
	}
	// The system cache (no prefix) must not see it either.
	if value := system.GetDefault(key, ""); "" != value {
		t.Errorf("system cache = %q", value)
	}

	// The entries live inside the configured cache folder.
	if got := system.Dir(); !strings.HasSuffix(strings.TrimRight(got, "/"), dir) {
		t.Errorf("cache dir = %q", got)
	}
}

// TestNewCacherWithoutACache checks the degraded case: no cache folder means no
// per-account cache either.
func TestNewCacherWithoutACache(t *testing.T) {
	appData := NewAppData(Options{})
	if nil != appData.NewCacher(nil) {
		t.Error("NewCacher without a cache must be nil")
	}
}

// TestNewCacherWithoutAnAccount checks the "_default_" (unprefixed) branch of
// Actions::Cacher() for a guest.
func TestNewCacherWithoutAnAccount(t *testing.T) {
	dir := t.TempDir()

	driver, err := storage.NewCacheFileDriver(dir)
	if nil != err {
		t.Fatalf("cache driver: %v", err)
	}
	appData := NewAppData(Options{Cache: storage.NewCache(driver)})

	guest := appData.NewCacher(nil)
	if nil == guest {
		t.Fatal("NewCacher returned nil")
	}
	if err := guest.Set(storage.ReadReceiptCacheKey("user@example.com", "INBOX", 1), "1"); nil != err {
		t.Fatalf("Set: %v", err)
	}
	if value := guest.GetDefault(storage.ReadReceiptCacheKey("user@example.com", "INBOX", 1), ""); "1" != value {
		t.Errorf("guest cacher = %q", value)
	}
}
