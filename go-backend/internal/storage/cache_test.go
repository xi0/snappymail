package storage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestKeyPathHelperValues(t *testing.T) {
	tests := []struct{ got, want string }{
		{SsoCacheKey("abc123"), "/Sso/Data/abc123/Login/"},
		{ReadReceiptCacheKey("user@example.com", "INBOX", 42), "/ReadReceipt/user@example.com/INBOX/42"},
		{LangCacheKey("da", false, "pluginhash", "0.0.0"), "/LangCache/pluginhash/da/App/0.0.0/"},
		{LangCacheKey("en", true, "pluginhash", "0.0.0"), "/LangCache/pluginhash/en/Admin/0.0.0/"},
		{PluginsJSCacheKey("pluginhash", "0.0.0"), "/PluginsJsCache/pluginhash/0.0.0/"},
		{SessionAdminKey("rand"), "/Session/AdminKey/" + SHA1("rand") + "/"},
	}

	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("key = %q, want %q", test.got, test.want)
		}
	}
}

func TestCacheFileDriverPathLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")

	driver, err := NewCacheFileDriver(dir)
	if nil != err {
		t.Fatalf("NewCacheFileDriver: %v", err)
	}

	// The cache folder is created with 0700 and holds the CACHEDIR.TAG marker.
	info, err := os.Stat(dir)
	if nil != err {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); 0o700 != mode {
		t.Errorf("cache folder mode = %04o, want 0700", mode)
	}
	tag, err := os.ReadFile(filepath.Join(dir, "CACHEDIR.TAG"))
	if nil != err {
		t.Fatalf("read CACHEDIR.TAG: %v", err)
	}
	if cachedirTag != string(tag) {
		t.Errorf("CACHEDIR.TAG = %q, want %q", tag, cachedirTag)
	}

	// The prefix is sanitised, padded to five characters and sharded.
	driver.SetPrefix("user@example.com")
	if want := "__/us/er/user_example_com/"; driver.prefix != want {
		t.Errorf("prefix = %q, want %q", driver.prefix, want)
	}
	driver.SetPrefix("ab")
	if want := "__/ab/__/ab___/"; driver.prefix != want {
		t.Errorf("short prefix = %q, want %q", driver.prefix, want)
	}
	driver.SetPrefix("")
	if "" != driver.prefix {
		t.Errorf("empty prefix = %q, want empty", driver.prefix)
	}

	// The entry path is the sha1 of the key, sharded two levels deep.
	key := ReadReceiptCacheKey("user@example.com", "INBOX", 42)
	if err := driver.Set(key, "1"); nil != err {
		t.Fatalf("Set: %v", err)
	}

	hashed := SHA1(key)
	path := filepath.Join(dir, hashed[0:2], hashed[2:4], hashed)
	content, err := os.ReadFile(path)
	if nil != err {
		t.Fatalf("read %q: %v", path, err)
	}
	if "1" != string(content) {
		t.Errorf("content = %q, want 1", content)
	}

	if !driver.Exists(key) {
		t.Error("Exists must report the entry")
	}
	if value, ok := driver.Get(key); !ok || "1" != value {
		t.Errorf("Get = %q, %v", value, ok)
	}

	// A key of three characters or less is not cached, like PHP.
	if err := driver.Set("abc", "x"); nil != err {
		t.Fatalf("Set(short): %v", err)
	}
	if driver.Exists("abc") {
		t.Error("a short key must not be cached")
	}

	if err := driver.Delete(key); nil != err {
		t.Fatalf("Delete: %v", err)
	}
	if driver.Exists(key) {
		t.Error("Delete must remove the entry")
	}

	// GC drops entries older than the given number of hours.
	if err := driver.Set("olduid", "1"); nil != err {
		t.Fatalf("Set: %v", err)
	}
	oldPath, _ := driver.fileName("olduid", false)
	past := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(oldPath, past, past); nil != err {
		t.Fatalf("chtimes: %v", err)
	}
	if !driver.GC(24) {
		t.Error("GC must report true")
	}
	if driver.Exists("olduid") {
		t.Error("GC must remove the old entry")
	}

	// PHP's driver always reports success; a zero age clears everything.
	if err := driver.Set("another", "1"); nil != err {
		t.Fatalf("Set: %v", err)
	}
	if !driver.GC(0) {
		t.Error("GC(0) must report true, like the PHP driver")
	}
	if driver.Exists("another") {
		t.Error("GC(0) must clear the cache folder")
	}
}

func TestCacheClient(t *testing.T) {
	driver, err := NewCacheFileDriver(filepath.Join(t.TempDir(), "cache"))
	if nil != err {
		t.Fatalf("NewCacheFileDriver: %v", err)
	}

	cache := NewCache(driver)
	if !cache.IsInited() {
		t.Fatal("a cache with a driver must report itself initialised")
	}

	if err := cache.Set("/Sso/Data/abc/Login/", "value"); nil != err {
		t.Fatalf("Set: %v", err)
	}
	if value, ok := cache.Get("/Sso/Data/abc/Login/"); !ok || "value" != value {
		t.Errorf("Get = %q, %v", value, ok)
	}
	if !cache.Exists("/Sso/Data/abc/Login/") {
		t.Error("Exists must report the entry")
	}
	if got := cache.GetDefault("/missing/key", "fallback"); "fallback" != got {
		t.Errorf("GetDefault = %q", got)
	}

	// GetClear removes the entry after reading it.
	if value, ok := cache.GetClear("/Sso/Data/abc/Login/"); !ok || "value" != value {
		t.Errorf("GetClear = %q, %v", value, ok)
	}
	if cache.Exists("/Sso/Data/abc/Login/") {
		t.Error("GetClear must remove the entry")
	}

	// The cache index is appended to every key, separated by a NUL byte.
	cache.SetCacheIndex("v1")
	if err := cache.Set("/PluginsJsCache/hash/0.0.0/", "js"); nil != err {
		t.Fatalf("Set: %v", err)
	}
	if !driver.Exists("/PluginsJsCache/hash/0.0.0/" + "\x00v1") {
		t.Error("the cache index must be appended to the driver key")
	}
	cache.SetCacheIndex("")
	if err := cache.Set("/PluginsJsCache/hash/0.0.0/", "js"); nil != err {
		t.Fatalf("Set: %v", err)
	}
	if !driver.Exists("/PluginsJsCache/hash/0.0.0/") {
		t.Error("without an index the bare key must be used")
	}

	// Timers and locks.
	if err := cache.SetTimer("/Timers/"); nil != err {
		t.Fatalf("SetTimer: %v", err)
	}
	if diff := time.Now().Unix() - cache.GetTimer("/Timers/"); 0 > diff || 5 < diff {
		t.Errorf("GetTimer = %d, want the current time", cache.GetTimer("/Timers/"))
	}
	if 0 != cache.GetTimer("/NoTimer/") {
		t.Error("GetTimer of a missing timer must be 0")
	}

	if cache.GetLock("/Lock/") {
		t.Error("a missing lock must not report itself held")
	}
	if err := cache.SetLock("/Lock/"); nil != err {
		t.Fatalf("SetLock: %v", err)
	}
	if !cache.GetLock("/Lock/") {
		t.Error("GetLock must report the held lock")
	}
	if err := cache.RemoveLock("/Lock/"); nil != err {
		t.Fatalf("RemoveLock: %v", err)
	}
	if cache.GetLock("/Lock/") {
		t.Error("RemoveLock must release the lock")
	}

	// Verify writes the current UTC hour.
	if !cache.Verify(false) {
		t.Error("Verify must report true on a writeable cache")
	}
	if !cache.Verify(true) {
		t.Error("Verify must accept the cached stamp")
	}
	if value, ok := cache.Get("__verify_key__"); !ok || value != time.Now().UTC().Format("2006-01-02-15") {
		t.Errorf("verify stamp = %q, %v", value, ok)
	}

	// An uninitialised client silently does nothing.
	empty := NewCache(nil)
	if empty.IsInited() {
		t.Error("a driver-less cache must not report itself initialised")
	}
	if err := empty.Set("key", "value"); nil != err {
		t.Errorf("Set without a driver = %v", err)
	}
	if _, ok := empty.Get("key"); ok {
		t.Error("Get without a driver must not report a value")
	}
	if empty.GC(24) {
		t.Error("GC without a driver must report false")
	}
}

func TestCacheTimerValue(t *testing.T) {
	driver, err := NewCacheFileDriver(filepath.Join(t.TempDir(), "cache"))
	if nil != err {
		t.Fatalf("NewCacheFileDriver: %v", err)
	}
	cache := NewCache(driver)

	for _, value := range []string{"1700000000", "not a number", ""} {
		if err := cache.Set("/Key/TIMER", value); nil != err {
			t.Fatalf("Set: %v", err)
		}
		want := int64(0)
		if "1700000000" == value {
			want = 1700000000
		}
		if got := cache.GetTimer("/Key"); got != want {
			t.Errorf("GetTimer(%q) = %d, want %d", value, got, want)
		}
	}

	// The stored value is the current unix time as a decimal string.
	if err := cache.SetTimer("/Other"); nil != err {
		t.Fatalf("SetTimer: %v", err)
	}
	value, ok := cache.Get("/Other/TIMER")
	if !ok {
		t.Fatal("SetTimer did not write the entry")
	}
	if _, err := strconv.ParseInt(value, 10, 64); nil != err {
		t.Errorf("timer value = %q, want a decimal number", value)
	}
}
