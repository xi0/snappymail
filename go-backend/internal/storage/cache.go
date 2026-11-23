package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cachedirTag is the content of the "CACHEDIR.TAG" marker every cache folder
// gets, see http://www.brynosaurus.com/cachedir/.
const cachedirTag = "Signature: 8a477f597d28d172789f06886806bc55"

// CacheDriver is the MailSo\Cache\DriverInterface subset the cache client uses.
type CacheDriver interface {
	// SetPrefix is setPrefix(): the per account path prefix.
	SetPrefix(prefix string)
	// Set stores a value; a missing entry is reported by Exists/Get.
	Set(key, value string) error
	// Exists reports whether key is cached.
	Exists(key string) bool
	// Get returns the cached value; ok is false when the entry is missing.
	Get(key string) (string, bool)
	// Delete drops key.
	Delete(key string) error
	// GC removes entries older than the given number of hours.
	GC(hours int) bool
}

// CacheFileDriver is MailSo\Cache\Drivers\File: the sha1 sharded file cache
// below the configured cache folder.
//
// The file name is <dir>/__/<prefix[0:2]>/<prefix[2:4]>/<padded prefix>/<sha1
// [0:2]>/<sha1[2:4]>/<sha1>, so a folder stays small and the cache is easy to
// purge by deleting the prefix folder.
type CacheFileDriver struct {
	// dir is sCacheFolder, with a trailing separator like PHP keeps it.
	dir string
	// prefix is sKeyPrefix: "" or "__/xx/yy/<prefix>/".
	prefix string
}

// NewCacheFileDriver is the MailSo\Cache\Drivers\File constructor: create the
// folder with 0700 and write the CACHEDIR.TAG marker when it is missing.
func NewCacheFileDriver(dir string) (*CacheFileDriver, error) {
	folder := strings.TrimRight(strings.TrimSpace(dir), `\/`) + "/"
	if err := Mkdir(folder); nil != err {
		return nil, err
	}

	tag := filepath.Join(folder, "CACHEDIR.TAG")
	if !isFile(tag) {
		if err := os.WriteFile(tag, []byte(cachedirTag), 0o600); nil != err {
			return nil, fmt.Errorf("storage: failed to create %s: %w", tag, err)
		}
	}

	return &CacheFileDriver{dir: folder}, nil
}

// Dir returns the cache folder.
func (d *CacheFileDriver) Dir() string { return d.dir }

// SetPrefix is File::setPrefix(): sanitise the prefix, pad it to at least five
// characters and turn it into a two level path shard.
func (d *CacheFileDriver) SetPrefix(prefix string) {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), `\/`)
	if "" == prefix {
		d.prefix = ""
		return
	}

	sanitised := make([]byte, 0, len(prefix))
	for index := 0; index < len(prefix); index++ {
		character := prefix[index]
		if 'a' <= character && 'z' >= character || 'A' <= character && 'Z' >= character ||
			'0' <= character && '9' >= character || '_' == character {
			sanitised = append(sanitised, character)
			continue
		}
		sanitised = append(sanitised, '_')
	}
	for len(sanitised) < 5 {
		sanitised = append(sanitised, '_')
	}

	value := string(sanitised)
	d.prefix = "__/" + value[0:2] + "/" + value[2:4] + "/" + value + "/"
}

// Set is File::Set().
func (d *CacheFileDriver) Set(key, value string) error {
	if 3 >= len(key) {
		return nil
	}

	path, err := d.fileName(key, true)
	if nil != err || "" == path {
		return err
	}

	return os.WriteFile(path, []byte(value), 0o600)
}

// Exists is File::Exists().
func (d *CacheFileDriver) Exists(key string) bool {
	path, err := d.fileName(key, false)
	return nil == err && "" != path && isFile(path)
}

// Get is File::Get().
func (d *CacheFileDriver) Get(key string) (string, bool) {
	path, err := d.fileName(key, false)
	if nil != err || "" == path {
		return "", false
	}

	content, readErr := os.ReadFile(path)
	if nil != readErr {
		return "", false
	}
	return string(content), true
}

// Delete is File::Delete().
func (d *CacheFileDriver) Delete(key string) error {
	path, err := d.fileName(key, false)
	if nil != err || "" == path {
		return err
	}
	if removeErr := os.Remove(path); nil != removeErr && !os.IsNotExist(removeErr) {
		return removeErr
	}
	return nil
}

// GC is File::GC(): drop the entries older than the given number of hours.
//
// Like the PHP driver it always reports success, and a zero (or negative) age
// clears the whole cache folder.
func (d *CacheFileDriver) GC(hours int) bool {
	age := time.Duration(hours) * time.Hour
	if 0 > age {
		age = 0
	}
	RecTimeDirRemove(d.dir, age)
	return true
}

// fileName is File::generateCachedFileName().
func (d *CacheFileDriver) fileName(key string, mkDir bool) (string, error) {
	if 3 >= len(key) {
		return "", nil
	}

	hashed := SHA1(key)
	path := d.dir + d.prefix + hashed[0:2] + "/" + hashed[2:4] + "/" + hashed

	if mkDir {
		if err := Mkdir(filepath.Dir(path)); nil != err {
			return "", err
		}
	}

	return path, nil
}

// Cache is MailSo\Cache\CacheClient: the driver plus the per account cache index
// that keeps a stale index from being served after an upgrade.
//
// It also satisfies domain.Cache (Get/Set/Delete), which the domain provider
// uses for the wildcard domain list.
type Cache struct {
	driver CacheDriver
	// index is sCacheIndex, already prefixed with the "\x00" separator.
	index string
}

// NewCache builds a cache client on top of a driver.
func NewCache(driver CacheDriver) *Cache {
	return &Cache{driver: driver}
}

// SetDriver is CacheClient::SetDriver().
func (c *Cache) SetDriver(driver CacheDriver) *Cache {
	c.driver = driver
	return c
}

// SetCacheIndex is CacheClient::SetCacheIndex(): the index is appended to every
// key, separated by a NUL byte.
func (c *Cache) SetCacheIndex(index string) *Cache {
	if "" == index {
		c.index = ""
		return c
	}
	c.index = "\x00" + index
	return c
}

// IsInited is CacheClient::IsInited().
func (c *Cache) IsInited() bool { return nil != c.driver }

// Dir returns the cache folder of the driver, or "" when it has none. It exists
// so a caller can build a second client (Actions::Cacher($oAccount)) on the same
// folder with another prefix, like PHP's CacheClient::SetDriver().
func (c *Cache) Dir() string {
	if nil == c || nil == c.driver {
		return ""
	}
	if file, ok := c.driver.(*CacheFileDriver); ok && nil != file {
		return file.Dir()
	}
	return ""
}

// Set is CacheClient::Set().
func (c *Cache) Set(key, value string) error {
	if nil == c.driver {
		return nil
	}
	return c.driver.Set(key+c.index, value)
}

// Exists is CacheClient::Exists().
func (c *Cache) Exists(key string) bool {
	return nil != c.driver && c.driver.Exists(key+c.index)
}

// Get is CacheClient::Get().
func (c *Cache) Get(key string) (string, bool) {
	if nil == c.driver {
		return "", false
	}
	return c.driver.Get(key + c.index)
}

// GetDefault is CacheClient::Get() with a default for a missing entry.
func (c *Cache) GetDefault(key, defaultValue string) string {
	if value, ok := c.Get(key); ok {
		return value
	}
	return defaultValue
}

// GetClear is CacheClient::Get($sKey, true): read the entry and drop it.
func (c *Cache) GetClear(key string) (string, bool) {
	value, ok := c.Get(key)
	if ok {
		_ = c.Delete(key)
	}
	return value, ok
}

// Delete is CacheClient::Delete().
func (c *Cache) Delete(key string) error {
	if nil == c.driver {
		return nil
	}
	return c.driver.Delete(key + c.index)
}

// SetTimer is CacheClient::SetTimer().
func (c *Cache) SetTimer(key string) error {
	return c.Set(key+"/TIMER", strconv.FormatInt(time.Now().Unix(), 10))
}

// GetTimer is CacheClient::GetTimer(): the stored unix time, or 0.
func (c *Cache) GetTimer(key string) int64 {
	value, ok := c.Get(key + "/TIMER")
	if !ok || "" == value {
		return 0
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if nil != err {
		return 0
	}
	return seconds
}

// SetLock is CacheClient::SetLock().
func (c *Cache) SetLock(key string) error { return c.Set(key+"/LOCK", "1") }

// RemoveLock is CacheClient::RemoveLock().
func (c *Cache) RemoveLock(key string) error { return c.Set(key+"/LOCK", "0") }

// GetLock is CacheClient::GetLock().
func (c *Cache) GetLock(key string) bool {
	value, ok := c.Get(key + "/LOCK")
	return ok && "1" == value
}

// Verify is CacheClient::Verify(): write and read the current UTC hour, so a
// broken cache driver is detected.
func (c *Cache) Verify(bCached bool) bool {
	if nil == c.driver {
		return false
	}

	stamp := time.Now().UTC().Format("2006-01-02-15")
	if bCached {
		if value, ok := c.Get("__verify_key__"); ok && stamp == value {
			return true
		}
	}

	return nil == c.Set("__verify_key__", stamp)
}

// GC is CacheClient::GC().
func (c *Cache) GC(hours int) bool {
	if nil == c.driver {
		return false
	}
	return c.driver.GC(hours)
}
