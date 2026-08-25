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

func TestConsoleUsesViewScopedDataRefresh(t *testing.T) {
	javascript, err := Static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(javascript)
	for _, required := range []string{
		`const viewResources=`,
		`overview:['overview','providers','nodes']`,
		`providers:['providers','nodes']`,
		`nodes:['nodes']`,
		`logs:['events','providers','nodes']`,
		`settings:['settings','service']`,
		`if(state.requests[name])return state.requests[name]`,
		`state.snapshots[name]!==snapshot`,
		`state.loadedAt[name]=Date.now()`,
		`background?unique.filter(name=>Date.now()-(state.loadedAt[name]||0)>=5000):unique`,
		`state.activeMutations>0`,
		`state.activeMutations>0||state.settingsDirty`,
		`active.matches('input,select,textarea')`,
		`state.expandedProviders[id])loadProviderRuntime(id)`,
		`const streamResources=`,
		`socket.close()`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("view-scoped refresh contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`async function load(){`,
		`Promise.all([api('/api/overview'),api('/api/providers'),api('/api/nodes'),api('/api/events'),api('/api/settings')`,
		`providers.map(async provider=>`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("global refresh implementation remains: %q", forbidden)
		}
	}
}
