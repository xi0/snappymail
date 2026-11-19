package handler

// The trivial actions of \RainLoop\Actions: DoPing and DoVersion.
//
// They exist mostly to prove the dispatch machinery end-to-end (an action with
// a constant result and one that reads an action parameter) and are the two
// actions the frontend can call before a session exists.

// RegisterSystem registers DoPing and DoVersion.
func RegisterSystem(d *Dispatcher) {
	d.Register("Ping", doPing)
	d.Register("Version", doVersion)
}

// doPing is Actions::DoPing(): a constant "Pong" result.
func doPing(ctx *Context) (any, error) {
	return ctx.DefaultResponse("Pong", nil), nil
}

// doVersion is Actions::DoVersion(): true when the client already runs
// APP_VERSION, which tells the frontend its cached assets are current.
func doVersion(ctx *Context) (any, error) {
	version := phpString(ctx.GetParam("version", ""))
	return ctx.DefaultResponse(ctx.Version() == version, nil), nil
}
