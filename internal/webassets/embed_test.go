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
