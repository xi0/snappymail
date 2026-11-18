package storage

import "strconv"

// The cache keys of RainLoop\KeyPathHelper.
//
// The strings are a compatibility contract: the PHP backend writes and reads the
// same entries in the same cache folder, so the shapes must not be changed
// (including the leading and trailing slashes and the "/" separated segments).

// SsoCacheKey is KeyPathHelper::SsoCacherKey(): the single-use SSO blob.
func SsoCacheKey(hash string) string {
	return "/Sso/Data/" + hash + "/Login/"
}

// ReadReceiptCacheKey is KeyPathHelper::ReadReceiptCache(): the "read receipt
// already sent" marker of a message.
func ReadReceiptCacheKey(email, folderFullName string, uid int) string {
	return "/ReadReceipt/" + email + "/" + folderFullName + "/" + strconv.Itoa(uid)
}

// LangCacheKey is KeyPathHelper::LangCache(): the compiled language file.
func LangCacheKey(language string, admin bool, pluginsHash, version string) string {
	scope := "App"
	if admin {
		scope = "Admin"
	}
	return "/LangCache/" + pluginsHash + "/" + language + "/" + scope + "/" + version + "/"
}

// PluginsJSCacheKey is KeyPathHelper::PluginsJsCache(): the compiled plugin
// JavaScript/CSS.
func PluginsJSCacheKey(pluginsHash, version string) string {
	return "/PluginsJsCache/" + pluginsHash + "/" + version + "/"
}

// SessionAdminKey is KeyPathHelper::SessionAdminKey(): the admin session marker.
func SessionAdminKey(rand string) string {
	return "/Session/AdminKey/" + SHA1(rand) + "/"
}
