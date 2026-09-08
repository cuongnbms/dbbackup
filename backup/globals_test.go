package backup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakePgDumpall puts a pg_dumpall shim first on PATH. The shim body decides
// whether the call succeeds and what it writes.
func fakePgDumpall(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pg_dumpall"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// onlyWithoutPasswords fails unless --no-role-passwords was passed, mimicking a
// server where the backup user cannot read pg_authid.
const onlyWithoutPasswords = `#!/bin/sh
out=""
nopw=0
while [ $# -gt 0 ]; do
  case "$1" in
    -f) out="$2"; shift ;;
    --no-role-passwords) nopw=1 ;;
  esac
  shift
done
if [ "$nopw" != "1" ]; then
  echo "pg_dumpall: error: query failed: ERROR:  permission denied for table pg_authid" >&2
  exit 1
fi
echo "CREATE ROLE app;" > "$out"
`

const alwaysFails = `#!/bin/sh
echo "pg_dumpall: error: connection refused" >&2
exit 1
`

const writesEmptyFile = `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -f) out="$2"; shift ;;
  esac
  shift
done
: > "$out"
`

func TestGlobalsArgs(t *testing.T) {
	got := globalsArgs(pgConn{Host: "h", Port: "5432", User: "u"}, "/backup/x/globals.sql", false)
	want := []string{"-h", "h", "-p", "5432", "-U", "u", "--globals-only", "-f", "/backup/x/globals.sql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGlobalsArgsNoRolePasswords(t *testing.T) {
	got := globalsArgs(pgConn{Host: "h", Port: "5432", User: "u"}, "/f", true)
	if got[len(got)-1] != "--no-role-passwords" {
		t.Fatalf("expected --no-role-passwords last, got %q", got)
	}
}

func TestDumpGlobalsFallsBackWhenPasswordsAreUnreadable(t *testing.T) {
	fakePgDumpall(t, onlyWithoutPasswords)
	dir := t.TempDir()
	if err := dumpGlobals(context.Background(), pgConn{Host: "h", Port: "1", User: "u"}, dir); err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "globals.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "CREATE ROLE") {
		t.Fatalf("globals.sql has unexpected content: %q", body)
	}
}

func TestDumpGlobalsReportsFirstErrorWhenBothAttemptsFail(t *testing.T) {
	fakePgDumpall(t, alwaysFails)
	err := dumpGlobals(context.Background(), pgConn{Host: "h", Port: "1", User: "u"}, t.TempDir())
	if err == nil {
		t.Fatal("expected error when pg_dumpall always fails")
	}
	if !strings.Contains(err.Error(), "pg_dumpall") {
		t.Fatalf("error should name the command, got %v", err)
	}
}

func TestDumpGlobalsRejectsEmptyOutput(t *testing.T) {
	fakePgDumpall(t, writesEmptyFile)
	err := dumpGlobals(context.Background(), pgConn{Host: "h", Port: "1", User: "u"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-output error, got %v", err)
	}
}
