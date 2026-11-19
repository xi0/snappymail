package handler

import (
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Context is the per-request action context a handler is called with, the Go
// counterpart of the $this of \RainLoop\Actions: the request, the parsed action
// parameters, the resolved account and the response helpers.
//
// Later steps attach more of the Actions surface (MailClient, SettingsProvider,
// Cacher, Plugins, …) here; step 9 only needs what DoPing/DoVersion and the
// dispatcher itself use.
type Context struct {
	dispatcher *Dispatcher
	session    Session

	// Request is the HTTP request of the action.
	Request *http.Request
	// Response is the writer the non-JSON endpoints use; the JSON envelope is
	// written by the dispatcher after the handler returns.
	Response http.ResponseWriter
	// Route is the parsed query-string path vector.
	Route *web.Route
	// Action is the action name without the "Do" prefix ("Version").
	Action string
	// Method is the PHP method name ("DoVersion"), which is also the plugin
	// additional-JSON lookup key.
	Method string

	params map[string]any
}

// Dispatcher returns the dispatcher running the action.
func (c *Context) Dispatcher() *Dispatcher { return c.dispatcher }

// Version is APP_VERSION.
func (c *Context) Version() string {
	if nil == c || nil == c.dispatcher {
		return ""
	}
	return c.dispatcher.version
}

// Account is Actions::getAccountFromToken(): the selected additional account,
// else the main account, else nil. A nil session (the dispatcher is not wired to
// the auth layer yet) yields (nil, nil).
func (c *Context) Account(throw bool) (*account.Account, error) {
	if nil == c || nil == c.session {
		return nil, nil
	}
	return c.session.GetAccountFromToken(throw)
}
