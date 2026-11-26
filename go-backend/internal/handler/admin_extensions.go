package handler

// The plugin/package administration of \RainLoop\Actions\AdminExtensions
// (Step 27).
//
// In PHP plugins are pulled from a remote repository into
// data/_data_/<name>/plugins/<id>/ and can be installed, enabled and configured
// at runtime. The Go backend registers plugins at build time instead (see the
// "Plugin model" section of SPEC.md), so this file lists the compiled-in plugins
// and rejects every mutation with a clear "build-time only" message.

import (
	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// buildTimeOnlyMessage is the reason returned by the rejected mutations.
const buildTimeOnlyMessage = "Plugins are compiled into the Go backend at build time; " +
	"install, enable and configure them by rebuilding with the plugin registered"

// RegisterAdminExtensions registers the DoAdminPackage*/DoAdminPlugin* actions.
func RegisterAdminExtensions(d *Dispatcher) {
	d.Register("AdminPackagesList", doAdminPackagesList)
	d.Register("AdminPackageInstall", doAdminPackageInstall)
	d.Register("AdminPackageDelete", doAdminPackageDelete)
	d.Register("AdminPluginDisable", doAdminPluginDisable)
	d.Register("AdminPluginLoad", doAdminPluginLoad)
	d.Register("AdminPluginSettingsUpdate", doAdminPluginSettingsUpdate)
}

// doAdminPackagesList is AdminExtensions::DoAdminPackagesList().
//
// Partial: Repository::getPackagesList() fetches the remote catalogue; the Go
// backend only knows the plugins compiled into the binary, so the remote list is
// empty (the frontend hides an empty list).
func doAdminPackagesList(ctx *Context) (any, error) {
	return ctx.DefaultResponse([]any{}, nil), nil
}

// doAdminPackageInstall is AdminExtensions::DoAdminPackageInstall(): rejected
// because packages are not installed at runtime.
func doAdminPackageInstall(ctx *Context) (any, error) {
	return ctx.FalseResponse(appctx.UnsupportedPluginPackage, buildTimeOnlyMessage, ""), nil
}

// doAdminPackageDelete is AdminExtensions::DoAdminPackageDelete(): rejected for
// the same reason.
func doAdminPackageDelete(ctx *Context) (any, error) {
	return ctx.FalseResponse(appctx.UnsupportedPluginPackage, buildTimeOnlyMessage, ""), nil
}

// doAdminPluginDisable is AdminExtensions::DoAdminPluginDisable(): rejected.
func doAdminPluginDisable(ctx *Context) (any, error) {
	return ctx.FalseResponse(appctx.UnsupportedPluginPackage, buildTimeOnlyMessage, ""), nil
}

// doAdminPluginLoad is AdminExtensions::DoAdminPluginLoad(): without a runtime
// plugin registry there is nothing to load, so the plugin is reported unknown.
func doAdminPluginLoad(ctx *Context) (any, error) {
	return ctx.FalseResponse(appctx.InvalidPluginPackage, buildTimeOnlyMessage, ""), nil
}

// doAdminPluginSettingsUpdate is AdminExtensions::DoAdminPluginSettingsUpdate():
// rejected because plugin configuration is not runtime editable.
func doAdminPluginSettingsUpdate(ctx *Context) (any, error) {
	return ctx.FalseResponse(appctx.CantSavePluginSettings, buildTimeOnlyMessage, ""), nil
}
