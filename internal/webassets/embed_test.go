package webassets

import (
	"strings"
	"testing"
)

func TestProviderDialogAccessibilityContract(t *testing.T) {
	source, err := Static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="provider-dialog-title"`,
		`aria-describedby="provider-dialog-description"`,
		`$('#p-name').focus({preventScroll:true})`,
		`event.key==='Escape'`,
		`event.key!=='Tab'`,
		`modalReturnFocus`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Provider dialog accessibility contract is missing %q", required)
		}
	}
}

func TestConsoleExperienceContracts(t *testing.T) {
	javascript, err := Static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := Static.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	markup, err := Static.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}

	for name, contract := range map[string][]string{
		"javascript": {
			`captureUIState`,
			`restoreUIState`,
			`data-provider-source`,
			`updateProviderFormVisibility`,
			`clear-node-filters`,
			`settingsDirty`,
			`beforeunload`,
		},
		"styles": {
			`.responsive-table`,
			`.settings-save`,
			`grid-template-columns:repeat(6,minmax(0,1fr))`,
			`bottom:calc(70px + env(safe-area-inset-bottom))`,
		},
		"markup": {
			`aria-label="主导航"`,
			`class="skip-link"`,
			`id="announcer"`,
		},
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
