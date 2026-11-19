// Package handler ports the JSON action surface of SnappyMail: the generic
// ?/Json entry point of \RainLoop\ServiceActions::ServiceJson(), the response
// envelope helpers of \RainLoop\Actions\Response and the Do* actions
// themselves.
//
// Step 9 provides the dispatch machinery and the two trivial actions
// (DoPing, DoVersion). Later steps add one file per PHP Actions trait
// (folders.go, messages.go, …) and register their handlers here:
//
//	registry.Register("Folders", doFolders)
//
// The action name never carries the "Do" prefix; PHP derives the method name
// with 'Do'.$sAction and this package derives the plugin hook names
// ("json.before-Folders", "json.after-Folders") and the DoPlugin* lookup
// ("DoFolders") from the same value.
//
// A Dispatcher is scoped to one request and not safe for concurrent use.
package handler

// ActionFunc is one Do<Action> handler. It receives the request scoped Context
// (action parameters, the resolved account, the response helpers) and returns
// the JSON envelope of a single action.
//
// A returned error becomes the "code"/"message"/"messageAdditional" fields of
// the envelope exactly like a thrown \RainLoop\Exceptions\ClientException does
// in PHP, so a handler reports domain failures with
// appctx.NewClientError(code, previous, message).
type ActionFunc func(*Context) (any, error)

// Registry maps an action name onto its handler, the Go stand-in for the
// method_exists()/is_callable() pair ServiceJson() checks before it calls
// $this->oActions->{'Do'.$sAction}.
type Registry struct {
	actions map[string]ActionFunc
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{actions: map[string]ActionFunc{}}
}

// Register adds (or replaces) the handler of an action. The action name has no
// "Do" prefix, matching the "Action" value the frontend sends.
func (r *Registry) Register(action string, fn ActionFunc) {
	if nil == r || nil == fn {
		return
	}
	if nil == r.actions {
		r.actions = map[string]ActionFunc{}
	}
	r.actions[action] = fn
}

// Lookup returns the handler of an action.
func (r *Registry) Lookup(action string) (ActionFunc, bool) {
	if nil == r {
		return nil, false
	}
	fn, ok := r.actions[action]
	return fn, ok
}

// Has reports whether an action has a handler.
func (r *Registry) Has(action string) bool {
	_, ok := r.Lookup(action)
	return ok
}
