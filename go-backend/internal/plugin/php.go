package plugin

// The small PHP helpers the ported plugin code needs. Keeping them here makes
// it obvious which quirks are reproduced on purpose.

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
)

// isFile mirrors PHP's is_file().
func isFile(name string) bool {
	info, err := os.Stat(name)
	return nil == err && info.Mode().IsRegular()
}

// isReadable mirrors PHP's is_readable(): a regular file the process may read.
func isReadable(name string) bool {
	info, err := os.Stat(name)
	if nil != err || !info.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(name)
	if nil != err {
		return false
	}
	_ = file.Close()
	return true
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// fileMTime is @filemtime($file): the modification time in seconds, or 0 when
// the file does not exist.
func fileMTime(name string) int64 {
	info, err := os.Stat(name)
	if nil != err {
		return 0
	}
	return info.ModTime().Unix()
}

// itoa64 is PHP's implicit (string) cast of an int in a concatenation.
func itoa64(value int64) string { return strconv.FormatInt(value, 10) }

// castLike is AbstractConfig::Set()'s "keep the type of the declared default"
// casting.
func castLike(current, value any) any {
	switch typed := current.(type) {
	case bool:
		return !phpEmpty(value)
	case int:
		return phpInt(value)
	case int64:
		return int64(phpInt(value))
	case float64:
		return phpFloat(value, typed)
	case string:
		return phpString(value)
	}
	// An undeclared key cannot reach this branch (Set ignores unknown keys).
	return phpString(value)
}

// phpFloat is PHP's (float) cast for the same types.
func phpFloat(value any, current float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if nil == err {
			return number
		}
		return 0
	}
	return current
}
