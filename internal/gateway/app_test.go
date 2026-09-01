package gateway

import (
	"errors"
	"strings"
	"testing"

	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
)

func TestFormatSurgeLineUsesValidatedKeywordAuthenticationSyntax(t *testing.T) {
	line := formatSurgeLine(M.Entry{
		DisplayName: "Provider · Node",
		SocksHost:   "socks.surge.eb",
		SocksPort:   1080,
		Username:    "surgeeb_user",
		Password:    "password_value",
		SupportUDP:  true,
	})
	want := "Provider · Node = socks5, socks.surge.eb, 1080, username=surgeeb_user, password=password_value, udp-relay=true"
	if line != want {
		t.Fatalf("Surge line = %q, want %q", line, want)
	}
	if strings.Contains(line, ", surgeeb_user, password_value,") {
		t.Fatal("Surge line regressed to positional authentication fields")
	}
}

func TestPersistenceRollbackFailureStopsDataPlaneAndPreservesBothErrors(t *testing.T) {
	persistErr := errors.New("persist candidate")
	rollbackErr := errors.New("restore previous runtime")
	stopped := false
	err := rollbackAfterPersistenceFailure(persistErr, rollbackErr, func() error {
		stopped = true
		return nil
	})
	if !stopped {
		t.Fatal("rollback failure did not fail closed")
	}
	if !errors.Is(err, persistErr) || !errors.Is(err, rollbackErr) || !strings.Contains(err.Error(), "data plane stopped") {
		t.Fatalf("combined rollback error = %v", err)
	}
}

func TestReorderedProvidersRequiresAnExactPermutation(t *testing.T) {
	providers := []Provider{{StableID: "first", Name: "First"}, {StableID: "second", Name: "Second"}, {StableID: "third", Name: "Third"}}
	reordered, changed, err := reorderedProviders(providers, []string{"third", "first", "second"})
	if err != nil || !changed {
		t.Fatalf("valid reorder changed=%t err=%v", changed, err)
	}
	if got := []string{reordered[0].StableID, reordered[1].StableID, reordered[2].StableID}; strings.Join(got, ",") != "third,first,second" {
		t.Fatalf("reordered IDs=%v", got)
	}
	for _, order := range [][]string{
		{"first", "second"},
		{"first", "first", "third"},
		{"first", "second", "unknown"},
	} {
		if _, _, err := reorderedProviders(providers, order); err == nil {
			t.Fatalf("invalid order %v was accepted", order)
		}
	}
	if _, changed, err := reorderedProviders(providers, []string{"first", "second", "third"}); err != nil || changed {
		t.Fatalf("unchanged order changed=%t err=%v", changed, err)
	}
}
