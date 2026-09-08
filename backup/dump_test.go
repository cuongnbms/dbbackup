package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePgTools puts shell shims named pg_dump/pg_restore first on PATH.
// pg_dump records its argv one-per-line into $ARGS_FILE and exits with $DUMP_EXIT.
func fakePgTools(t *testing.T, dumpExit string) string {
	t.Helper()
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args.txt")
	dump := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\ntouch \"$(eval echo \\$$(($#-1)))\" 2>/dev/null; exit $DUMP_EXIT\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(dump), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pg_restore"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("DUMP_EXIT", dumpExit)
	return argsFile
}

func TestDumpDatabasePassesExcludeTablesAsSeparateArgv(t *testing.T) {
	argsFile := fakePgTools(t, "0")
	conn := pgConn{Host: "h", Port: "1", User: "u", Password: "p"}
	if err := dumpDatabase(context.Background(), conn, "demo", t.TempDir(), []string{"users", "logs"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var excludes []string
	for _, a := range argv {
		if strings.HasPrefix(a, "--exclude-table=") {
			excludes = append(excludes, a)
		}
	}
	if len(excludes) != 2 || excludes[0] != "--exclude-table=users" || excludes[1] != "--exclude-table=logs" {
		t.Fatalf("exclude flags not passed as separate argv: %q", argv)
	}
	if argv[len(argv)-1] != "demo" {
		t.Fatalf("dbname must be last argv, got %q", argv)
	}
}

func TestDumpDatabaseReturnsErrorWhenPgDumpFails(t *testing.T) {
	fakePgTools(t, "3")
	conn := pgConn{Host: "h", Port: "1", User: "u", Password: "p"}
	err := dumpDatabase(context.Background(), conn, "demo", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "pg_dump demo") {
		t.Fatalf("expected pg_dump failure to propagate, got %v", err)
	}
}
