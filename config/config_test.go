package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadConfigDefaults(t *testing.T) {
	cfg, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackupDir != "/backup" {
		t.Fatalf("expected default backup_dir, got %q", cfg.BackupDir)
	}
}

func TestReadConfigRejectsKeepZero(t *testing.T) {
	if _, err := ReadConfig(write(t, "cron: '0 0 * * *'\n")); err == nil {
		t.Fatal("expected error when keep is missing/0")
	}
}

func TestReadConfigRejectsMissingCron(t *testing.T) {
	if _, err := ReadConfig(write(t, "keep: 2\n")); err == nil {
		t.Fatal("expected error when cron is missing")
	}
}
