package gateway

import (
	"strings"
	"testing"

	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
)

func TestFormatSurgeLineUsesValidatedKeywordAuthenticationSyntax(t *testing.T) {
	line := formatSurgeLine(M.Entry{
		DisplayName: "Provider · Node",
		Username:    "surgeeb_user",
		Password:    "password_value",
		SupportUDP:  true,
	}, Config{SocksAdvertise: "127.0.0.1", SocksPort: 1080})
	want := "Provider · Node = socks5, 127.0.0.1, 1080, username=surgeeb_user, password=password_value, udp-relay=true"
	if line != want {
		t.Fatalf("Surge line = %q, want %q", line, want)
	}
	if strings.Contains(line, ", surgeeb_user, password_value,") {
		t.Fatal("Surge line regressed to positional authentication fields")
	}
}
