package service

// The per-request webmail state the index page (internal/pages) reads:
// GetLanguage/GetTheme/compileCss/compileLanguage/StaticI18N bound to the
// request session.

import (
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/pages"
)

// indexActions implements pages.Actions for one request.
type indexActions struct {
	appData *AppData
	session *Session
	admin   bool
}

// IndexActions builds the pages.Actions factory of the AppData service. main
// passes the returned factory to pages.New, so the index page shares the
// cookie/account stack with ?/AppData and ?/Json.
func (a *AppData) IndexActions(w http.ResponseWriter, r *http.Request, admin bool) pages.Actions {
	return &indexActions{
		appData: a,
		session: a.session(w, r),
		admin:   admin,
	}
}

// Language is Actions\Localization::GetLanguage().
func (i *indexActions) Language() string {
	resolved, _ := accounts(i.session)
	return i.appData.language(i.admin, resolved, acceptLanguage(i.session))
}

// Theme is Actions\Themes::GetTheme().
func (i *indexActions) Theme() string {
	resolved, _ := accounts(i.session)
	return i.appData.theme(i.admin, resolved)
}

// CompileCSS is Actions\Themes::compileCss().
func (i *indexActions) CompileCSS(theme string) (string, error) {
	return i.appData.CompileCSS(theme, i.admin, false)
}

// CompileLanguage is Actions\Localization::compileLanguage().
func (i *indexActions) CompileLanguage(language string) (string, error) {
	return i.appData.CompileLanguage(language, i.admin)
}

// StaticI18N is Actions\Localization::StaticI18N(): it uses the webmail
// language even in the admin area, like PHP.
func (i *indexActions) StaticI18N(key string) string {
	resolved, _ := accounts(i.session)
	language := i.appData.language(false, resolved, acceptLanguage(i.session))
	return i.appData.StaticI18N(language, key)
}

// LoggedIn is Actions::getAccountFromToken(false) != null.
func (i *indexActions) LoggedIn() bool {
	resolved, _ := accounts(i.session)
	return nil != resolved
}
