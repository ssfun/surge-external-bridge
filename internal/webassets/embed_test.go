package webassets

import (
	"strings"
	"testing"
)

func TestEmbeddedConsoleAssets(t *testing.T) {
	styles, err := Static.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	markup, err := Static.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}

	javascript, err := Static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for name, contract := range map[string][]string{
		"styles": {
			`.responsive-table`,
			`.settings-save`,
			`grid-template-columns:repeat(6,minmax(0,1fr))`,
			`bottom:calc(70px + env(safe-area-inset-bottom))`,
		},
		"markup": {
			`class="skip-link"`,
			`src="/app.js"`,
			`href="/styles.css"`,
		},
		"javascript": {`createApp`, `/api/overview`},
	} {
		var source string
		switch name {
		case "javascript":
			source = string(javascript)
		case "styles":
			source = string(styles)
		case "markup":
			source = string(markup)
		}
		for _, required := range contract {
			if !strings.Contains(source, required) {
				t.Fatalf("%s experience contract is missing %q", name, required)
			}
		}
	}
}
