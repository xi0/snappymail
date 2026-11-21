package pages

// Port of ServiceActions::compileTemplates(): the view templates of the install
// tree, inlined as <template id="…"> elements inside the index page.
//
// The order is the PHP order (Components, the User/Admin views, Common), with
// the plugin templates appended by Plugins\Manager::CompileTemplate(). A
// template name is the basename without ".html" (plus "Component") and is
// stripped of every non alphanumeric character for the id.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Templates is the ordered name -> file list of compileTemplates().
//
// It is the Go equivalent of the by-reference $aTemplates array: replacing an
// existing name keeps its position, like PHP array assignment does.
type Templates struct {
	order []string
	files map[string]string
}

// NewTemplates builds an empty list.
func NewTemplates() *Templates {
	return &Templates{files: map[string]string{}}
}

// Set adds or replaces a template.
func (t *Templates) Set(name, file string) {
	if _, ok := t.files[name]; !ok {
		t.order = append(t.order, name)
	}
	t.files[name] = file
}

// Names returns the template names in insertion order.
func (t *Templates) Names() []string { return t.order }

// File returns the file of a template name.
func (t *Templates) File(name string) string { return t.files[name] }

// The two rewrites of the template body.
var (
	// templateNamePattern is '/[^a-zA-Z0-9]/'.
	templateNamePattern = regexp.MustCompile(`[^a-zA-Z0-9]`)
	// scriptTagPattern is '/<(\/?)script/i'.
	scriptTagPattern = regexp.MustCompile(`(?i)<(/?)script`)
)

// compileTemplates is ServiceActions::compileTemplates().
func (r *Renderer) compileTemplates(admin bool) string {
	templates := NewTemplates()

	viewsDir := filepath.Join(r.versionRoot(), "app", "templates", "Views")

	area := "User"
	if admin {
		area = "Admin"
	}

	for _, dir := range []string{"Components", area, "Common"} {
		suffix := ""
		if "Components" == dir {
			suffix = "Component"
		}

		matches, err := filepath.Glob(filepath.Join(viewsDir, dir, "*.html"))
		if nil != err {
			continue
		}

		// filepath.Glob sorts ascending, like glob() does.
		for _, file := range matches {
			name := strings.TrimSuffix(filepath.Base(file), ".html") + suffix
			templates.Set(name, file)
		}
	}

	if plugins, ok := r.options.Plugins.(TemplatePlugins); ok && nil != r.options.Plugins {
		plugins.CompileTemplate(templates, admin)
	}

	var builder strings.Builder
	for _, name := range templates.Names() {
		content, err := os.ReadFile(templates.File(name))
		if nil != err {
			continue
		}

		builder.WriteString(`<template id="`)
		builder.WriteString(templateNamePattern.ReplaceAllString(name, ""))
		builder.WriteString(`">`)
		builder.WriteString(scriptTagPattern.ReplaceAllString(string(content), "<${1}x-script"))
		builder.WriteString(`</template>`)
	}

	return strings.ReplaceAll(builder.String(), "&nbsp;", "\xC2\xA0")
}
