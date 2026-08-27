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
