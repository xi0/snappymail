package service

// ServiceActions::ServiceLang(): ?/Lang/0/{App,Admin}/<language>/<version>/

import (
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Lang is ServiceActions::ServiceLang(): the compiled language JavaScript.
func (a *AppData) Lang(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")

	result := ""

	// !empty($this->aPaths[3])
	if 3 < len(route.Paths) && "" != route.Paths[3] {
		admin := 2 < len(route.Paths) && "Admin" == route.Paths[2]
		language := a.ValidateLanguage(route.Paths[3], admin)

		cacheKey := ""
		if a.config().GetBool("cache", "system_data", true) {
			cacheKey = storage.LangCacheKey(language, admin, a.PluginsHash(), a.options.Version)
			if cache := a.options.Cache; nil != cache {
				if value, ok := cache.Get(cacheKey); ok {
					result = value
				}
			}
		}

		if "" == result {
			if compiled, err := a.CompileLanguage(language, admin); nil == err {
				result = compiled
			}
			if "" != cacheKey && nil != a.options.Cache {
				_ = a.options.Cache.Set(cacheKey, result)
			}
		}
	}

	_, _ = w.Write([]byte(result))
	return true
}
