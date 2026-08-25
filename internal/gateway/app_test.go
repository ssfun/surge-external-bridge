package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	M "github.com/ssfun/vless2surge/internal/mihomo"
)

func TestFormatSurgeLineUsesValidatedKeywordAuthenticationSyntax(t *testing.T) {
	line := formatSurgeLine(M.Entry{
		DisplayName: "Provider · Node",
		Username:    "v2s_user",
		Password:    "password_value",
		SupportUDP:  true,
	}, Config{SocksAdvertise: "127.0.0.1", SocksPort: 1080})
	want := "Provider · Node = socks5, 127.0.0.1, 1080, username=v2s_user, password=password_value, udp-relay=true"
	if line != want {
		t.Fatalf("Surge line = %q, want %q", line, want)
	}
	if strings.Contains(line, ", v2s_user, password_value,") {
		t.Fatal("Surge line regressed to positional authentication fields")
	}
}

func TestMigrationNoticePersistsWhileReadOnlyBackupExists(t *testing.T) {
	dir := t.TempDir()
	application := &App{store: NewStore(dir)}
	if notice := application.MigrationNotice(); notice != "" {
		t.Fatalf("fresh data directory reported migration: %q", notice)
	}
	backup := filepath.Join(dir, "migration-v1-readonly")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "config.json"), []byte(`{}`), 0o400); err != nil {
		t.Fatal(err)
	}
	notice := application.MigrationNotice()
	if !strings.Contains(notice, "Mihomo Core 架构迁移") || !strings.Contains(notice, "凭据已整体变化") {
		t.Fatalf("migration notice is incomplete: %q", notice)
	}
}
