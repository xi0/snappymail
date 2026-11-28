package plugins

import (
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/plugin"
)

func TestAllIsACopyOfTheBuildTimeRegistry(t *testing.T) {
	list := All()
	if nil == list {
		t.Fatal("All() must never return nil (main passes it to the manager)")
	}

	// The returned slice must be a copy, so a caller cannot register a plugin
	// by mutating it.
	if 0 < len(list) {
		list[0] = nil
		if nil == All()[0] {
			t.Error("All() must return a copy of the registry")
		}
	}
}

func TestEveryRegisteredFactoryBuildsAPlugin(t *testing.T) {
	for index, factory := range All() {
		if nil == factory {
			t.Errorf("factory %d is nil", index)
			continue
		}

		instance := factory()
		if nil == instance {
			t.Errorf("factory %d returned nil", index)
			continue
		}
		if "" == instance.Name() {
			t.Errorf("factory %d returns a plugin without a name", index)
		}
	}
}

// TestCaldavIsTheOnlyPlugin documents the scope of the ported feature set
// (SPEC.md: "only the caldav plugin should be available"). Step 29 adds its
// factory to buildTime; this test then only guards against accidental
// registrations of the plugins that are explicitly out of scope.
func TestCaldavIsTheOnlyPlugin(t *testing.T) {
	allowed := map[string]bool{"caldav": true}

	for _, factory := range All() {
		if nil == factory {
			continue
		}
		instance := factory()
		if nil == instance {
			continue
		}
		if !allowed[instance.Name()] {
			t.Errorf("plugin %q is registered but out of scope for the Go backend", instance.Name())
		}
	}

	// Sanity check of the registry shape: the type is a plugin.Factory slice,
	// so step 29 only adds `caldav.New` (or an equivalent constructor).
	var typed []plugin.Factory = All()
	if 0 != len(typed) {
		t.Logf("%d plugin(s) registered", len(typed))
	}
}
