package service

// ServiceActions::ServiceCss():
// ?/Css/0/{User,Admin}/-/<theme>/-/<ts>/Hash/-/Json/

import (
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Css is ServiceActions::ServiceCss(): the theme stylesheet, or the
// [theme, css] JSON pair when the path ends in "Json".
func (a *AppData) Css(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	admin := 2 < len(route.Paths) && "Admin" == route.Paths[2]
	asJSON := 9 < len(route.Paths) && "Json" == route.Paths[9]

	if asJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}

	theme := ""
	result := ""

	// !empty($this->aPaths[4])
	if 4 < len(route.Paths) && "" != route.Paths[4] {
		theme = a.ValidateTheme(route.Paths[4])

		appDebug := a.config().GetBool("debug", "enable", false)
		minify := ""
		if !appDebug && !a.config().GetBool("debug", "css", false) {
			minify = "min"
		}

		cacheKey := ""
		if !appDebug && a.config().GetBool("cache", "system_data", true) {
			cacheKey = "/CssCache/" + a.PluginsHash() + "/" + theme + "/" + a.options.Version + "/" + minify
			if cache := a.options.Cache; nil != cache {
				if value, ok := cache.Get(cacheKey); ok {
					result = value
				}
			}
		}

		if "" == result {
			if css, err := a.CompileCSS(theme, admin, false); nil == err {
				result = css
				if "" != cacheKey && nil != a.options.Cache {
					_ = a.options.Cache.Set(cacheKey, result)
				}
			}
		}
	}

	if asJSON {
		encoded, err := encodeJSON([]any{theme, result})
		if nil != err {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		_, _ = w.Write(encoded)
		return true
	}

	_, _ = w.Write([]byte(result))
	return true
}
