package service

// ServiceActions::ServicePlugins(): ?/Plugins/0/{User,Admin}/<etag>/

import (
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Plugins is ServiceActions::ServicePlugins(): the compiled plugin JavaScript.
func (a *AppData) Plugins(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	admin := 2 < len(route.Paths) && "Admin" == route.Paths[2]

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")

	appDebug := a.config().GetBool("debug", "enable", false)
	minify := ""
	if !appDebug && !a.config().GetBool("debug", "javascript", false) {
		minify = "min"
	}

	result := ""

	cacheKey := ""
	if !appDebug && a.config().GetBool("cache", "system_data", true) {
		cacheKey = storage.PluginsJSCacheKey(a.PluginsHash(), a.options.Version) + minify
		if cache := a.options.Cache; nil != cache {
			if value, ok := cache.Get(cacheKey); ok {
				result = value
			}
		}
	}

	if "" == result {
		result = a.CompilePluginsJS(admin, "min" == minify)
		if "" != cacheKey && nil != a.options.Cache {
			_ = a.options.Cache.Set(cacheKey, result)
		}
	}

	_, _ = w.Write([]byte(result))
	return true
}
