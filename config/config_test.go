package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestReadConfigParsesNotifyEvents(t *testing.T) {
	cfg, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nnotify:\n  events:\n    - backup_failed\n    - upload_failed\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Notify.Events) != 2 || cfg.Notify.Events[0] != "backup_failed" {
		t.Fatalf("unexpected events: %q", cfg.Notify.Events)
	}
}

func TestReadConfigRejectsUnknownNotifyEvent(t *testing.T) {
	_, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nnotify:\n  events:\n    - backup_faild\n"))
	if err == nil {
		t.Fatal("expected an error for a misspelled event name")
	}
}

func TestReadConfigAllowsNoNotifyBlock(t *testing.T) {
	cfg, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Notify.Events) != 0 {
		t.Fatalf("expected notification to be off by default, got %q", cfg.Notify.Events)
	}
}

// The shipped config.yaml is copied into the image, so a mistake in it breaks
// every container that does not mount its own.
func TestShippedConfigIsValid(t *testing.T) {
	cfg, err := ReadConfig(filepath.Join("..", "config.yaml"))
	if err != nil {
		t.Fatalf("shipped config.yaml is invalid: %v", err)
	}
	if cfg.Keep < 7 {
		t.Fatalf("shipped keep is %d; a daily cron needs at least a week of history", cfg.Keep)
	}
}

func TestReadConfigParsesAWSS3(t *testing.T) {
	cfg, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nremote_backup:\n  aws_s3:\n    enable: true\n    keep: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	s3 := cfg.RemoteBackup.AWSS3
	if !s3.Enable || s3.Keep != 5 {
		t.Fatalf("unexpected aws_s3 config: %+v", s3)
	}
}

func TestReadConfigRejectsNegativeAWSS3Keep(t *testing.T) {
	_, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nremote_backup:\n  aws_s3:\n    keep: -1\n"))
	if err == nil {
		t.Fatal("expected an error for a negative aws_s3.keep")
	}
}

func TestReadConfigParsesExcludeTableData(t *testing.T) {
	cfg, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nexclude_table_data:\n  demo:\n    - public.django_session\n    - public.log_*\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ExcludeTableData["demo"]
	if len(got) != 2 || got[0] != "public.django_session" || got[1] != "public.log_*" {
		t.Fatalf("unexpected exclude_table_data: %q", got)
	}
}

// exclude_tables was removed: it dropped the table definition too, which left
// dependent views and foreign keys unrestorable. Silently ignoring the old key
// would grow every archive back to full size without a word, so it is an error.
func TestReadConfigRejectsRemovedExcludeTablesKey(t *testing.T) {
	_, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nexclude_tables:\n  demo:\n    - users\n"))
	if err == nil {
		t.Fatal("expected an error for the removed exclude_tables key")
	}
	if !strings.Contains(err.Error(), "exclude_table_data") {
		t.Fatalf("error must name the replacement key, got: %v", err)
	}
}

func TestReadConfigRejectsMalformedDatabasePattern(t *testing.T) {
	_, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nexclude_databases:\n  - 'test_['\n"))
	if err == nil {
		t.Fatal("expected an error for a malformed exclude_databases pattern")
	}
}

func TestReadConfigParsesMinUploadSpeed(t *testing.T) {
	cfg, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nremote_backup:\n  min_upload_speed_kbps: 512\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteBackup.MinUploadSpeedKBps != 512 {
		t.Fatalf("got %d", cfg.RemoteBackup.MinUploadSpeedKBps)
	}
}

// A negative rate would yield a negative deadline, which cancels every upload
// before it starts. 0 is the way to switch the deadline off.
func TestReadConfigRejectsNegativeMinUploadSpeed(t *testing.T) {
	_, err := ReadConfig(write(t, "keep: 2\ncron: '0 0 * * *'\nremote_backup:\n  min_upload_speed_kbps: -1\n"))
	if err == nil || !strings.Contains(err.Error(), "min_upload_speed_kbps") {
		t.Fatalf("expected an error naming min_upload_speed_kbps, got %v", err)
	}
}
