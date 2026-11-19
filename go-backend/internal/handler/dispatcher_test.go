package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// testVersion is APP_VERSION in the tests.
const testVersion = "0.0.0"

// testEpoch is the fixed clock of the envelope.
var testEpoch = time.Unix(1700000000, 0)

// fakeSession is the per-request session the token and mismatch-log checks use.
type fakeSession struct {
	token   string
	account *account.Account
}

func (f *fakeSession) ConnectionToken() string { return f.token }

func (f *fakeSession) GetAccountFromToken(throw bool) (*account.Account, error) {
	if nil == f.account && throw {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "")
	}
	return f.account, nil
}

// fakePlugins records the hooks it runs and serves the DoPlugin* actions.
type fakePlugins struct {
	hooks      []string
	additional map[string]any
}

func (p *fakePlugins) HasAdditionalJSON(method string) bool {
	_, ok := p.additional[method]
	return ok
}

func (p *fakePlugins) RunAdditionalJSON(method string) (any, error) {
	return p.additional[method], nil
}

func (p *fakePlugins) RunHook(name string, args ...any) {
	p.hooks = append(p.hooks, name)
	// Mutate the response like a real json.after-* hook does.
	if strings.HasPrefix(name, "json.after-") && 1 == len(args) {
		if target, ok := args[0].(*map[string]any); ok {
			(*target)["hooked"] = true
		}
	}
}

func (p *fakePlugins) has(name string) bool {
	for _, hook := range p.hooks {
		if hook == name {
			return true
		}
	}
	return false
}

// fakeAdmin is the admin session stub.
type fakeAdmin struct {
	loggedIn bool
	err      error
	calls    int
}

func (a *fakeAdmin) IsLoggedIn(throw bool) (bool, error) {
	a.calls++
	if nil != a.err {
		return false, a.err
	}
	if a.loggedIn {
		return true, nil
	}
	if throw {
		return false, appctx.NewClientError(appctx.AuthError, nil, "")
	}
	return false, nil
}

// newTestDispatcher builds a dispatcher with the fixed clock and the given
// dependencies.
func newTestDispatcher(session Session, plugins Plugins, admin Admin) *Dispatcher {
	return NewDispatcher(Options{
		Version: testVersion,
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Plugins: plugins,
		Admin:   admin,
		Now:     func() time.Time { return testEpoch },
	})
}

// serve runs the dispatcher and decodes the JSON envelope.
func serve(t *testing.T, dispatcher *Dispatcher, request *http.Request, route *web.Route) map[string]any {
	t.Helper()

	recorder := httptest.NewRecorder()
	if !dispatcher.Serve(recorder, request, route) {
		t.Fatal("Serve must report the request as handled")
	}

	if got := recorder.Header().Get("Content-Type"); "application/json; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); nil != err {
		t.Fatalf("response is not JSON (%v): %s", err, recorder.Body.String())
	}
	return response
}

// postJSON builds an application/json POST request.
func postJSON(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// nonAdminRoute is the path vector of a "?/Json/…" request.
func nonAdminRoute(paths ...string) *web.Route {
	return &web.Route{Paths: append([]string{"Json"}, paths...)}
}

// codeOf returns the notification code of the envelope (0 when absent). It
// accepts the int shapes of a directly built envelope and the float64 of a
// JSON-decoded one.
func codeOf(response map[string]any) int {
	switch value := response["code"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return 0
}

func TestDoVersionViaPathGet(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	request := httptest.NewRequest(http.MethodGet, "/?/Json/0/Version/", nil)
	response := serve(t, dispatcher, request, nonAdminRoute("0", "Version"))

	if "Version" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	// A GET request has no version parameter, so APP_VERSION does not match "".
	if false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
	if got, _ := response["epoch"].(float64); int64(got) != testEpoch.Unix() {
		t.Errorf("epoch = %v, want %d", response["epoch"], testEpoch.Unix())
	}
}

func TestDoVersionViaJSONBody(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Version","version":"0.0.0","XToken":"tok"}`),
		nonAdminRoute("0"))

	if "Version" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
}

func TestDoVersionViaFormBody(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	request := httptest.NewRequest(http.MethodPost, "/?/Json/0/",
		strings.NewReader("Action=Version&version=0.0.0&XToken=tok"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response := serve(t, dispatcher, request, nonAdminRoute("0"))

	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
}

func TestDoPingIsConstant(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	request := httptest.NewRequest(http.MethodGet, "/?/Json/0/Ping/", nil)
	response := serve(t, dispatcher, request, nonAdminRoute("0", "Ping"))

	if "Ping" != response["Action"] || "Pong" != response["Result"] {
		t.Errorf("response = %v", response)
	}
}

func TestUnknownActionIsUnknownError(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Nope","XToken":"tok"}`),
		nonAdminRoute("0"))

	if "Nope" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if appctx.UnknownError != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.UnknownError)
	}
	if false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
	if message, _ := response["message"].(string); "UnknownError[999]" != message {
		t.Errorf("message = %q", message)
	}
	if _, ok := response["ExceptionCode"]; !ok {
		t.Error("ExceptionCode must be present")
	}
}

func TestActionUnknownWhenMissing(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"XToken":"tok"}`), nonAdminRoute("0"))

	if "Unknown" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if appctx.InvalidInputArgument != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.InvalidInputArgument)
	}
	if additional, _ := response["messageAdditional"].(string); "Action unknown" != additional {
		t.Errorf("messageAdditional = %q", additional)
	}
}

func TestInvalidActionIsNotAString(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":["Version"],"XToken":"tok"}`), nonAdminRoute("0"))

	if "Unknown" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if appctx.InvalidInputArgument != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.InvalidInputArgument)
	}
	if additional, _ := response["messageAdditional"].(string); "Invalid Action" != additional {
		t.Errorf("messageAdditional = %q", additional)
	}
}

func TestInvalidBodyIsRejected(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `"just a string"`), nonAdminRoute("0"))

	if "Unknown" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if appctx.InvalidInputArgument != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.InvalidInputArgument)
	}
	if additional, _ := response["messageAdditional"].(string); "Invalid request body" != additional {
		t.Errorf("messageAdditional = %q", additional)
	}
}

func TestEmptyBodyOnGetIsAccepted(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	// A GET with no body at all is the normal case; the action comes from the
	// path. Version must be dispatched, not rejected as an invalid body.
	request := httptest.NewRequest(http.MethodGet, "/?/Json/0/Version/", nil)
	response := serve(t, dispatcher, request, nonAdminRoute("0", "Version"))

	if "Version" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if appctx.InvalidInputArgument == codeOf(response) {
		t.Error("an empty GET body must not be an invalid body")
	}
}

func TestMissingTokenOnPostIsInvalidToken(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Version"}`), nonAdminRoute("0"))

	if "Version" != response["Action"] {
		t.Errorf("Action = %v", response["Action"])
	}
	if appctx.InvalidToken != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.InvalidToken)
	}
	if additional, _ := response["messageAdditional"].(string); "XToken mismatch" != additional {
		t.Errorf("messageAdditional = %q", additional)
	}
}

func TestHeaderTokenMismatch(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	request := postJSON("/?/Json/0/", `{"Action":"Version"}`)
	request.Header.Set("X-SM-Token", "wrong")

	response := serve(t, dispatcher, request, nonAdminRoute("0"))

	if appctx.InvalidToken != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.InvalidToken)
	}
	if additional, _ := response["messageAdditional"].(string); "HTTP Token mismatch" != additional {
		t.Errorf("messageAdditional = %q", additional)
	}
}

func TestMatchingHeaderTokenPasses(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	request := postJSON("/?/Json/0/", `{"Action":"Version","version":"0.0.0"}`)
	request.Header.Set("X-SM-Token", "tok")

	response := serve(t, dispatcher, request, nonAdminRoute("0"))

	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
}

func TestLogoutSkipsTheTokenCheck(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	// No token at all, but Logout is exempt and reaches the (unregistered)
	// action, so the response is the UnknownError instead of InvalidToken.
	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Logout"}`), nonAdminRoute("0"))

	if appctx.InvalidToken == codeOf(response) {
		t.Error("Logout must skip the token check")
	}
	if appctx.UnknownError != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.UnknownError)
	}
}

func TestAdminActionRefusedWithoutSession(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	route := &web.Route{Admin: true, Paths: []string{"Json", "0"}}
	response := serve(t, dispatcher,
		postJSON("/?/admin/Json/0/", `{"Action":"AdminSettingsGet","XToken":"tok"}`), route)

	if appctx.AuthError != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.AuthError)
	}
}

func TestAdminActionAllowedWithSession(t *testing.T) {
	session := &fakeSession{token: "tok"}
	admin := &fakeAdmin{loggedIn: true}
	dispatcher := newTestDispatcher(session, nil, admin)
	dispatcher.Register("AdminProbe", func(*Context) (any, error) {
		return map[string]any{"Action": "", "Result": "ok"}, nil
	})

	route := &web.Route{Admin: true, Paths: []string{"Json", "0"}}
	response := serve(t, dispatcher,
		postJSON("/?/admin/Json/0/", `{"Action":"AdminProbe","XToken":"tok"}`), route)

	if "AdminProbe" != response["Action"] || "ok" != response["Result"] {
		t.Errorf("response = %v", response)
	}
	if 0 == admin.calls {
		t.Error("the admin session must be consulted")
	}
}

func TestAdminLoginIsExemptFromTheGate(t *testing.T) {
	session := &fakeSession{token: "tok"}
	admin := &fakeAdmin{}
	dispatcher := newTestDispatcher(session, nil, admin)
	dispatcher.Register("AdminLogin", func(*Context) (any, error) {
		return map[string]any{"Action": "", "Result": "login"}, nil
	})

	route := &web.Route{Admin: true, Paths: []string{"Json", "0"}}
	response := serve(t, dispatcher,
		postJSON("/?/admin/Json/0/", `{"Action":"AdminLogin","XToken":"tok"}`), route)

	if "login" != response["Result"] {
		t.Errorf("AdminLogin must not be gated, got %v", response)
	}
	if 0 != admin.calls {
		t.Error("AdminLogin must not consult the admin session")
	}
}

func TestAdminActionOutsideAdminAreaIsUnknown(t *testing.T) {
	session := &fakeSession{token: "tok"}
	admin := &fakeAdmin{loggedIn: true}
	dispatcher := newTestDispatcher(session, nil, admin)

	// The user area never has the DoAdmin* actions, so this is UnknownError and
	// not an admin-session refusal.
	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"AdminSettingsGet","XToken":"tok"}`),
		nonAdminRoute("0"))

	if appctx.UnknownError != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.UnknownError)
	}
	if 0 != admin.calls {
		t.Error("the admin session must not be consulted outside the admin area")
	}
}

func TestHooksRunAroundTheAction(t *testing.T) {
	session := &fakeSession{token: "tok"}
	plugins := &fakePlugins{}
	dispatcher := newTestDispatcher(session, plugins, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Ping","XToken":"tok"}`), nonAdminRoute("0"))

	if !plugins.has("json.before-Ping") {
		t.Error("json.before-Ping must run")
	}
	if !plugins.has("json.after-Ping") {
		t.Error("json.after-Ping must run")
	}
	if true != response["hooked"] {
		t.Errorf("the json.after-* hook did not mutate the response: %v", response)
	}
}

func TestPluginAdditionalJSON(t *testing.T) {
	session := &fakeSession{token: "tok"}
	plugins := &fakePlugins{additional: map[string]any{
		"DoPluginThing": map[string]any{"Action": "", "Result": "from-plugin"},
	}}
	dispatcher := newTestDispatcher(session, plugins, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"PluginThing","XToken":"tok"}`), nonAdminRoute("0"))

	if "PluginThing" != response["Action"] || "from-plugin" != response["Result"] {
		t.Errorf("response = %v", response)
	}
	if !plugins.has("json.before-PluginThing") {
		t.Error("json.before-PluginThing must run")
	}
}

func TestGetRawKeyParameter(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)
	dispatcher.Register("Probe", func(ctx *Context) (any, error) {
		return ctx.DefaultResponse(ctx.GetParam("RawKey", ""), nil), nil
	})

	request := httptest.NewRequest(http.MethodGet, "/?/Json/0/Probe/the-raW-key/", nil)
	// GET has no body, so the action and RawKey come from the path vector.
	response := serve(t, dispatcher, request,
		&web.Route{Paths: []string{"Json", "0", "Probe", "the-raW-key"}})

	if "the-raW-key" != response["Result"] {
		t.Errorf("RawKey = %v", response["Result"])
	}
}

func TestHandlerErrorBecomesClientError(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)
	dispatcher.Register("Boom", func(*Context) (any, error) {
		return nil, appctx.NewClientError(appctx.CantGetMessage, errors.New("imap down"), "")
	})

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Boom","XToken":"tok"}`), nonAdminRoute("0"))

	if appctx.CantGetMessage != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.CantGetMessage)
	}
	if message, _ := response["message"].(string); "CantGetMessage[202]" != message {
		t.Errorf("message = %q", message)
	}
}

func TestHandlerPanicBecomesUnknownError(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)
	dispatcher.Register("Panics", func(*Context) (any, error) {
		panic("boom")
	})

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Panics","XToken":"tok"}`), nonAdminRoute("0"))

	if appctx.UnknownError != codeOf(response) {
		t.Errorf("code = %v, want %d", response["code"], appctx.UnknownError)
	}
}

func TestDoVersionMatchesPHPEnvelopeShape(t *testing.T) {
	session := &fakeSession{token: "tok"}
	dispatcher := newTestDispatcher(session, nil, nil)

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"Version","version":"9.9.9","XToken":"tok"}`),
		nonAdminRoute("0"))

	// PHP: DefaultResponse(false) adds code=0 and message=''.
	if false != response["Result"] {
		t.Errorf("Result = %v", response["Result"])
	}
	if _, ok := response["code"]; !ok {
		t.Error("a false result must carry code")
	}
	if message, ok := response["message"]; !ok || "" != message {
		t.Errorf("message = %v", response["message"])
	}
}

func TestSessionAccountIsAvailableToHandlers(t *testing.T) {
	main := account.NewMainAccount()
	main.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")

	session := &fakeSession{token: "tok", account: &main.Account}
	dispatcher := newTestDispatcher(session, nil, nil)
	dispatcher.Register("WhoAmI", func(ctx *Context) (any, error) {
		resolved, err := ctx.Account(true)
		if nil != err {
			return nil, err
		}
		if nil == resolved {
			return ctx.DefaultResponse("", nil), nil
		}
		return ctx.DefaultResponse(resolved.Email(), nil), nil
	})

	response := serve(t, dispatcher,
		postJSON("/?/Json/0/", `{"Action":"WhoAmI","XToken":"tok"}`), nonAdminRoute("0"))

	if "user@example.com" != response["Result"] {
		t.Errorf("Result = %v", response["Result"])
	}
}
