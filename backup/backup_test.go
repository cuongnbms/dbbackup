package backup

import (
	"context"
	"reflect"
	"testing"

	"dbbackup/config"
)

func TestIsExcluded(t *testing.T) {
	// A pattern without a metacharacter still has to match exactly, so an
	// existing exclude_databases list keeps behaving as it always did.
	cases := []struct {
		name     string
		patterns []string
		want     bool
	}{
		{"postgres", []string{"postgres", "template0"}, true},
		{"app", []string{"postgres"}, false},
		{"test_a", []string{"test_*"}, true},
		{"atest_a", []string{"test_*"}, false},
		{"test_", []string{"test_*"}, true},
		{"shard_7", []string{"shard_[0-9]"}, true},
		{"shard_x", []string{"shard_[0-9]"}, false},
		{"app_dev", []string{"app_de?"}, true},
		{"app", []string{"prod", "app_*"}, false},
	}
	for _, c := range cases {
		if got := isExcluded(c.name, c.patterns); got != c.want {
			t.Errorf("isExcluded(%q, %q) = %v, want %v", c.name, c.patterns, got, c.want)
		}
	}
}

// The table keeps its definition and loses only its rows, and the exclusion
// reaches partitions and inheritance children, which is what makes it usable
// for a partitioned log table.
func TestDumpArgsExcludeTableDataAreSeparateArgs(t *testing.T) {
	got := dumpArgs(pgConn{Host: "h", Port: "5432", User: "u"}, "app", "/backup/x/app.backup", []string{"public.users", "public.log_*"})
	want := []string{
		"-h", "h", "-p", "5432", "-U", "u", "-F", "c", "-f", "/backup/x/app.backup",
		"--exclude-table-data-and-children=public.users",
		"--exclude-table-data-and-children=public.log_*",
		"app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDumpArgsNoExclude(t *testing.T) {
	got := dumpArgs(pgConn{Host: "h", Port: "5432", User: "u"}, "app", "/f", nil)
	if got[len(got)-1] != "app" || len(got) != 11 {
		t.Fatalf("unexpected args: %q", got)
	}
}

func TestConnURLEscapesPassword(t *testing.T) {
	c := pgConn{Host: "db", Port: "5432", User: "u", Password: "p a'ss@w/ord", SSLMode: "disable"}
	got := c.url()
	want := "postgres://u:p%20a%27ss%40w%2Ford@db:5432/postgres?sslmode=disable"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestPgConnFromEnvMissing(t *testing.T) {
	t.Setenv("PG_HOST", "")
	t.Setenv("PG_PORT", "5432")
	t.Setenv("PG_USER", "u")
	t.Setenv("PG_PASSWORD", "p")
	t.Setenv("PG_SSLMODE", "disable")
	if _, err := pgConnFromEnv(); err == nil {
		t.Fatal("expected error for missing PG_HOST")
	}
}

func TestPerformBackupAlwaysReturnsAReport(t *testing.T) {
	// A missing PG_HOST fails at the very first step, which is the earliest
	// possible return and therefore the strictest check that the report is
	// never nil.
	t.Setenv("PG_HOST", "")
	t.Setenv("PG_PORT", "5432")
	t.Setenv("PG_USER", "u")
	t.Setenv("PG_PASSWORD", "p")
	t.Setenv("PG_SSLMODE", "disable")

	cfg := &config.Config{BackupDir: t.TempDir(), Keep: 1, Cron: "@daily"}
	rep, err := PerformBackup(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected an error when PG_HOST is unset")
	}
	if rep == nil {
		t.Fatal("PerformBackup must never return a nil report")
	}
	if rep.Duration <= 0 {
		t.Fatal("the report should carry how long the run took")
	}
	if rep.Stage != "connect" {
		t.Fatalf("expected stage %q for a missing PG_HOST, got %q", "connect", rep.Stage)
	}
}
