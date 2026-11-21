package themes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

func TestCompileCSSShippedTheme(t *testing.T) {
	versionRoot := t.TempDir()

	set := Set{
		VersionRoot: versionRoot,
		Paths:       httpx.WebPaths{Version: "0.0.0", VersionRoot: versionRoot},
	}

	writeCSS(t, versionRoot, "themes/Gazellemail/styles.css",
		"body{background:url(images/bg.png)}")

	css, err := set.CompileCSS("Gazellemail", false, false, nil)
	if nil != err {
		t.Fatalf("CompileCSS: %v", err)
	}

	base := "/snappymail/v/0.0.0/themes/Gazellemail/"
	if !strings.Contains(css, "url("+base+"images/bg.png)") {
		t.Errorf("css = %q, want the theme base in the url()", css)
	}

	// url(data:...) must stay untouched.
	writeCSS(t, versionRoot, "themes/Gazellemail/styles.css",
		"body{background:url(data:image/png;base64,AAA)}")
	css, _ = set.CompileCSS("Gazellemail", false, false, nil)
	if !strings.Contains(css, "url(data:image/png;base64,AAA)") {
		t.Errorf("css = %q, a data: url must not be rewritten", css)
	}
}

func TestCompileCSSCustomAndNextcloudBases(t *testing.T) {
	versionRoot := t.TempDir()
	indexRoot := t.TempDir()
	nextcloudRoot := t.TempDir()

	set := Set{
		VersionRoot:      versionRoot,
		IndexRoot:        indexRoot,
		NextcloudRoot:    nextcloudRoot,
		NextcloudWebRoot: "/nextcloud",
		Paths:            httpx.WebPaths{AppPath: "/mail", Version: "0.0.0", VersionRoot: versionRoot},
	}

	writeCSS(t, indexRoot, "themes/Company/styles.css", "a{background:url(./logo.png)}")
	css, err := set.CompileCSS("Company@custom", false, false, nil)
	if nil != err {
		t.Fatalf("CompileCSS(@custom): %v", err)
	}
	if !strings.Contains(css, "url(/mail/themes/Company/logo.png)") {
		t.Errorf("css = %q, want the custom theme base /mail/", css)
	}

	writeCSS(t, nextcloudRoot, "themes/Rainloop/snappymail/style.css", "a{background:url(./x.png)}")
	css, err = set.CompileCSS("Rainloop@nextcloud", false, false, nil)
	if nil != err {
		t.Fatalf("CompileCSS(@nextcloud): %v", err)
	}
	if !strings.Contains(css, "url(/nextcloud/x.png)") {
		t.Errorf("css = %q, want the @nextcloud base", css)
	}
}

func TestCompileCSSRejectsLess(t *testing.T) {
	versionRoot := t.TempDir()
	writeCSS(t, versionRoot, "themes/Old/styles.less", "body{}")

	set := Set{VersionRoot: versionRoot, Paths: httpx.WebPaths{Version: "0.0.0"}}

	_, err := set.CompileCSS("Old", false, false, nil)
	if !errors.Is(err, ErrLessUnsupported) {
		t.Errorf("err = %v, want ErrLessUnsupported", err)
	}
}

func TestCompileCSSAppendsPluginCSS(t *testing.T) {
	versionRoot := t.TempDir()
	writeCSS(t, versionRoot, "themes/Gazellemail/styles.css", "body{}")

	set := Set{VersionRoot: versionRoot, Paths: httpx.WebPaths{Version: "0.0.0"}}

	css, err := set.CompileCSS("Gazellemail", false, false, fakePluginCSS{"/* plugin */"})
	if nil != err {
		t.Fatalf("CompileCSS: %v", err)
	}
	if !strings.Contains(css, "/* plugin */") {
		t.Errorf("css = %q, want the plugin stylesheet", css)
	}
}

// fakePluginCSS is Plugins\Manager::CompileCss().
type fakePluginCSS struct{ css string }

// CompileCSS returns the configured stylesheet.
func (f fakePluginCSS) CompileCSS(_ bool, _ *bool, _ bool) string {
	return f.css
}

// writeCSS creates (or replaces) a stylesheet below root.
func writeCSS(t *testing.T, root, relative, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", path, err)
	}
}
