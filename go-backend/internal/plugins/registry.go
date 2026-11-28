// Package plugins is the build-time plugin registry of the Go backend.
//
// PHP installs plugins at runtime: the admin panel downloads a package into
// data/_data_/<name>/plugins/<id>/ and the Manager includes its index.php. The
// Go backend cannot do that (there is no PHP interpreter), so a plugin is
// compiled in and listed here instead; enabling or disabling it is a change to
// plugins.enabled_list plus a rebuild (see the "Plugin model" section of
// SPEC.md).
//
// This file is the single place where a plugin import is added, which is why
// the step that ports a plugin touches nothing else in the wiring:
//
//	import "github.com/xi0/snappymail/go-backend/internal/plugins/caldav"
//
//	var buildTime = []plugin.Factory{
//		caldav.New,
//	}
package plugins

import (
	"github.com/xi0/snappymail/go-backend/internal/plugin"
	"github.com/xi0/snappymail/go-backend/internal/plugins/caldav"
)

// buildTime lists the plugins compiled into the binary, in registration order.
//
// Only "caldav" is in scope for the Go backend (see SPEC.md). An empty list
// means the binary has no plugins at all, which behaves exactly like
// plugins.enable = Off. Step 29 registers the CalDAV plugin; steps 30–32 add its
// event and invite actions.
var buildTime = []plugin.Factory{
	caldav.New,
}

// All returns the compiled-in plugins. The caller (main) hands the slice to
// plugin.Options.Registry; the manager instantiates them, filtered by
// plugins.enabled_list.
func All() []plugin.Factory {
	result := make([]plugin.Factory, len(buildTime))
	copy(result, buildTime)
	return result
}
