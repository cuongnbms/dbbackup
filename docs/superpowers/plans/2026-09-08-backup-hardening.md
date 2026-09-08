# Backup Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a db-backup artifact restorable into a fresh cluster, and make a failed run impossible to miss.

**Architecture:** `pg_dumpall --globals-only` joins the dump loop so roles and grants travel with the databases. `PerformBackup` returns a `*Report` describing the run; `main.go` maps that report onto notification events and posts them to Slack and Discord through a new `notify` package, which `backup` never imports. Retention and zip compression are configuration-level changes.

**Tech Stack:** Go 1.22.5, standard library only (no new dependencies), `gopkg.in/yaml.v3` for config, `pg_dump`/`pg_dumpall`/`pg_restore` 18 from the runtime image.

**Spec:** `docs/superpowers/specs/2026-09-08-backup-hardening-design.md`

## Global Constraints

- Module path is `dbbackup`; import internal packages as `dbbackup/backup`, `dbbackup/config`, `dbbackup/notify`.
- Go 1.22.5. No new third-party dependencies — tests use `net/http/httptest`, `t.Setenv`, and PATH shims, never a mocking framework.
- Tests live in the same package as the code under test (white box), matching `backup/dump_test.go`.
- Run the full suite with `make test` (`go vet ./... && go test ./...`). It must pass before every commit.
- **Docker runs on the `devtuf` server over SSH (`ssh devtuf ...`), never locally.** This is a hard rule from `~/.claude/CLAUDE.md`. `make build` is a Docker command; do not run it locally.
- Notification delivery must never fail a backup run: log the error and continue.
- Do not change the existing behaviour that an upload failure is still returned as the fatal error from `PerformBackup`.

---

### Task 1: Dump cluster globals

A per-database `pg_dump` contains no roles, so a restore into an empty cluster fails on every `ALTER ... OWNER TO` and `GRANT`. This task adds `pg_dumpall --globals-only` to the flow, with one unconditional retry without role passwords for servers where the backup user cannot read `pg_authid`.

**Files:**
- Create: `backup/globals.go`
- Create: `backup/globals_test.go`
- Modify: `backup/backup.go` (inside `PerformBackup`, after the `defer os.RemoveAll(workDir)` line, before the `for _, dbname := range databases` loop)
- Modify: `README.md`

**Interfaces:**
- Consumes: `pgConn` struct (`backup/backup.go:21`) with fields `Host, Port, User, Password, SSLMode string`.
- Produces: `globalsArgs(conn pgConn, outFile string, noPasswords bool) []string` and `dumpGlobals(ctx context.Context, conn pgConn, workDir string) error`. `dumpGlobals` writes `<workDir>/globals.sql`.

- [ ] **Step 1: Write the failing tests**

Create `backup/globals_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./backup/ -run 'Globals' -v`
Expected: FAIL — `undefined: globalsArgs`, `undefined: dumpGlobals`.

- [ ] **Step 3: Write the implementation**

Create `backup/globals.go`:

```go
package backup

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// globalsArgs builds the pg_dumpall argv. With noPasswords the dump omits role
// password hashes, which needs no privileged read of pg_authid.
func globalsArgs(conn pgConn, outFile string, noPasswords bool) []string {
	args := []string{"-h", conn.Host, "-p", conn.Port, "-U", conn.User, "--globals-only", "-f", outFile}
	if noPasswords {
		args = append(args, "--no-role-passwords")
	}
	return args
}

// dumpGlobals writes the cluster-level objects that no per-database pg_dump
// contains: roles, their password hashes, tablespaces and cluster-wide grants.
// Without them a restore into an empty cluster fails on every OWNER TO and
// GRANT naming a role that was never created.
//
// On any failure it retries once without role passwords. The retry is
// unconditional rather than gated on recognising a permission error, because
// pg_dumpall's message varies by version and locale and matching it would rot
// silently into "never retry". If the retry also fails the first error is
// returned: it is the informative one.
func dumpGlobals(ctx context.Context, conn pgConn, workDir string) error {
	outFile := filepath.Join(workDir, "globals.sql")

	log.Println("Backing up cluster globals")
	if firstErr := runPgDumpall(ctx, conn, outFile, false); firstErr != nil {
		if err := runPgDumpall(ctx, conn, outFile, true); err != nil {
			return firstErr
		}
		log.Printf("Warning: globals dumped without role passwords: %v", firstErr)
	}

	// There is no pg_restore --list equivalent for a plain SQL file, so the
	// check is that the file is not empty.
	info, err := os.Stat(outFile)
	if err != nil {
		return fmt.Errorf("stat globals dump: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("globals dump is empty: %s", outFile)
	}
	log.Println("Backup completed for cluster globals")
	return nil
}

func runPgDumpall(ctx context.Context, conn pgConn, outFile string, noPasswords bool) error {
	cmd := exec.CommandContext(ctx, "pg_dumpall", globalsArgs(conn, outFile, noPasswords)...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+conn.Password, "PGSSLMODE="+conn.SSLMode)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_dumpall --globals-only: %w: %s", err, output)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./backup/ -run 'Globals' -v`
Expected: PASS, all five tests.

- [ ] **Step 5: Call it from PerformBackup**

In `backup/backup.go`, inside `PerformBackup`, insert this immediately after the `defer os.RemoveAll(workDir)` line and before the `for _, dbname := range databases {` loop:

```go
	if err := dumpGlobals(ctx, conn, workDir); err != nil {
		return err
	}
```

It runs before the per-database loop so that a permission problem fails the run in a second rather than after a twenty-minute dump.

- [ ] **Step 6: Document the password-hash caveat**

In `README.md`, add this paragraph to the section describing what a backup contains (immediately before the `## Development` heading):

```markdown
Each archive also contains `globals.sql` — the cluster's roles, tablespaces and
grants, which no per-database `pg_dump` captures. Restore it first, before any
`pg_restore`, or every `OWNER TO` and `GRANT` will fail.

`globals.sql` contains SCRAM password hashes. With `ENCRYPT_KEY` unset those
hashes sit in plaintext inside the archive and in blob storage. If the backup
user cannot read `pg_authid` the dump falls back to `--no-role-passwords`
automatically and logs a warning; restored roles then need their passwords set
by hand.
```

- [ ] **Step 7: Run the full suite**

Run: `make test`
Expected: PASS, `go vet` clean.

- [ ] **Step 8: Commit**

```bash
git add backup/globals.go backup/globals_test.go backup/backup.go README.md
git commit -m "feat(backup): dump cluster globals so a restore into a fresh cluster works"
```

---

### Task 2: Choose the zip compression method per file

`pg_dump` custom-format files are already zlib-compressed; deflating them again costs a full pass over the whole archive for about 8%. Measured on a 101 MB table: 24M compressed by `pg_dump`, 22M after the zip deflates it again. `globals.sql` is plain text and must stay compressed.

**Files:**
- Modify: `backup/zip.go:11-58` (`ZipFolder` and `addFile`)
- Modify: `backup/zip_test.go`

**Interfaces:**
- Consumes: nothing from other tasks. `globals.sql` is produced by Task 1 but this task only depends on the file extension.
- Produces: `compressionFor(name string) uint16`. `ZipFolder(source, target string) error` keeps its existing signature.

- [ ] **Step 1: Write the failing tests**

Append to `backup/zip_test.go`:

```go
func TestZipFolderStoresDumpsAndDeflatesText(t *testing.T) {
	src := t.TempDir()
	// Incompressible bytes, so a deflate attempt cannot be mistaken for a store.
	blob := make([]byte, 4096)
	for i := range blob {
		blob[i] = byte(i * 7919 % 251)
	}
	if err := os.WriteFile(filepath.Join(src, "demo.backup"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "globals.sql"), []byte(strings.Repeat("CREATE ROLE app;\n", 200)), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "out.zip")
	if err := ZipFolder(src, target); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(target)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	methods := map[string]uint16{}
	for _, f := range r.File {
		methods[f.Name] = f.Method
	}
	if methods["demo.backup"] != zip.Store {
		t.Fatalf("demo.backup should be stored, got method %d", methods["demo.backup"])
	}
	if methods["globals.sql"] != zip.Deflate {
		t.Fatalf("globals.sql should be deflated, got method %d", methods["globals.sql"])
	}
}

func TestZipFolderPreservesModTime(t *testing.T) {
	src := t.TempDir()
	path := filepath.Join(src, "a.backup")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "out.zip")
	if err := ZipFolder(src, target); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(target)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Zip timestamps have two-second granularity.
	if delta := r.File[0].Modified.Sub(want); delta > 2*time.Second || delta < -2*time.Second {
		t.Fatalf("modtime not preserved: got %v, want %v", r.File[0].Modified, want)
	}
}
```

Add `"strings"` and `"time"` to the import block of `backup/zip_test.go`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./backup/ -run 'ZipFolder' -v`
Expected: FAIL — `demo.backup should be stored, got method 8` and `modtime not preserved: got 1979-11-30 ...`.

- [ ] **Step 3: Write the implementation**

In `backup/zip.go`, replace the `filepath.Walk` callback's `addFile` call and the `addFile` function. The walk callback becomes:

```go
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return addFile(archive, relPath, path, info)
	})
```

And replace `addFile` with:

```go
// compressionFor stores pg_dump custom-format files as they are: they are
// already zlib-compressed, so deflating them again costs a full pass over the
// archive for about 8%. Everything else — globals.sql in particular — is plain
// text and compresses well.
func compressionFor(name string) uint16 {
	if filepath.Ext(name) == ".backup" {
		return zip.Store
	}
	return zip.Deflate
}

func addFile(archive *zip.Writer, name, path string, info os.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer, err := archive.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   compressionFor(name),
		Modified: info.ModTime(),
	})
	if err != nil {
		return err
	}
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("add %s to zip: %w", name, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./backup/ -run 'ZipFolder' -v`
Expected: PASS, including the pre-existing `TestZipFolderRoundTrip` and `TestZipFolderMissingSource`.

- [ ] **Step 5: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backup/zip.go backup/zip_test.go
git commit -m "perf(backup): store already-compressed dumps in the zip and keep their modtime"
```

---

### Task 3: The `notify` package

A self-contained package that posts a message to Slack and Discord webhooks. It imports nothing from `backup` or `config`, so it can be built and tested on its own.

**Files:**
- Create: `notify/notify.go`
- Create: `notify/notify_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Event string` with constants `BackupFailed`, `UploadFailed`, `RemoteCleanupFailed`, `BackupSucceeded` (values `"backup_failed"`, `"upload_failed"`, `"remote_cleanup_failed"`, `"backup_succeeded"`).
  - `func Valid(name string) bool`
  - `type Message struct { Event Event; Title, Body string }`
  - `type Sender interface { Send(ctx context.Context, msg Message) error; Name() string }`
  - `func SendersFromEnv() []Sender` — reads `SLACK_WEBHOOK_URL` and `DISCORD_WEBHOOK_URL`.
  - `func New(senders []Sender, events []string, secret string) *Notifier`
  - `func (n *Notifier) Notify(ctx context.Context, msg Message)` — returns nothing by design.

- [ ] **Step 1: Write the failing tests**

Create `notify/notify_test.go`:

```go
package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// capture starts a server that records the decoded JSON body of every request.
func capture(t *testing.T, status int) (*httptest.Server, *[]map[string]string) {
	t.Helper()
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("body is not a JSON object: %s", body)
		}
		got = append(got, payload)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestValidRejectsUnknownEvent(t *testing.T) {
	if !Valid("backup_failed") {
		t.Fatal("backup_failed must be valid")
	}
	if Valid("backup_faild") {
		t.Fatal("a typo must not be accepted")
	}
}

func TestSlackSenderUsesTextField(t *testing.T) {
	srv, got := capture(t, 200)
	s := newWebhookSender("slack", srv.URL, "text", srv.Client())
	if err := s.Send(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0]["text"] != "T\nB" {
		t.Fatalf("unexpected payload: %+v", *got)
	}
}

func TestDiscordSenderUsesContentField(t *testing.T) {
	srv, got := capture(t, 200)
	s := newWebhookSender("discord", srv.URL, "content", srv.Client())
	if err := s.Send(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0]["content"] != "T\nB" {
		t.Fatalf("unexpected payload: %+v", *got)
	}
}

func TestSenderTreatsNon2xxAsError(t *testing.T) {
	srv, _ := capture(t, 500)
	s := newWebhookSender("slack", srv.URL, "text", srv.Client())
	if err := s.Send(context.Background(), Message{Title: "T"}); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestSenderRespectsClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	s := newWebhookSender("slack", srv.URL, "text", &http.Client{Timeout: 30 * time.Millisecond})
	if err := s.Send(context.Background(), Message{Title: "T"}); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestNotifierSkipsDisabledEvent(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupSucceeded, Title: "T"})
	if len(*got) != 0 {
		t.Fatalf("disabled event was delivered: %+v", *got)
	}
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T"})
	if len(*got) != 1 {
		t.Fatalf("enabled event was not delivered: %+v", *got)
	}
}

func TestNotifierTruncatesLongBody(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: strings.Repeat("x", 5000)})
	text := (*got)[0]["text"]
	if len(text) > maxBody+100 {
		t.Fatalf("body was not truncated: %d characters", len(text))
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("truncation should be visible in the message: %q", text[len(text)-60:])
	}
}

func TestNotifierRedactsSecret(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "hunter2")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "dsn=postgres://u:hunter2@db"})
	text := (*got)[0]["text"]
	if strings.Contains(text, "hunter2") {
		t.Fatalf("secret leaked to the webhook: %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Fatalf("expected a redaction marker: %q", text)
	}
}

func TestNotifierKeepsGoingWhenASenderFails(t *testing.T) {
	bad, _ := capture(t, 500)
	good, got := capture(t, 200)
	n := New([]Sender{
		newWebhookSender("slack", bad.URL, "text", bad.Client()),
		newWebhookSender("discord", good.URL, "content", good.Client()),
	}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T"})
	if len(*got) != 1 {
		t.Fatalf("a failing sender stopped the next one: %+v", *got)
	}
}

func TestNotifierWithNoSecretDoesNotRedactEverything(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "plain body"})
	if (*got)[0]["text"] != "T\nplain body" {
		t.Fatalf("unexpected payload: %+v", *got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./notify/ -v`
Expected: FAIL — the package does not exist yet (`no Go files in .../notify`), or once the file exists, `undefined: newWebhookSender`.

- [ ] **Step 3: Write the implementation**

Create `notify/notify.go`:

```go
// Package notify delivers backup notifications to chat webhooks. It knows
// nothing about backups: callers hand it a finished Message.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Event is a named thing that happened during a backup run and that a user can
// subscribe to.
type Event string

const (
	BackupFailed        Event = "backup_failed"
	UploadFailed        Event = "upload_failed"
	RemoteCleanupFailed Event = "remote_cleanup_failed"
	BackupSucceeded     Event = "backup_succeeded"
)

// maxBody caps the message body. Discord rejects payloads over 2000 characters
// and a failed pg_dump returns its whole output, which can exceed that and
// lose the entire alert.
const maxBody = 1500

// requestTimeout bounds one webhook call. Notification must never hold up a
// backup run.
const requestTimeout = 10 * time.Second

// Valid reports whether name is an event a user may subscribe to.
func Valid(name string) bool {
	switch Event(name) {
	case BackupFailed, UploadFailed, RemoteCleanupFailed, BackupSucceeded:
		return true
	}
	return false
}

// Message is one notification, already formatted by the caller.
type Message struct {
	Event Event
	Title string
	Body  string
}

// Sender delivers a Message to one destination.
type Sender interface {
	Send(ctx context.Context, msg Message) error
	Name() string
}

// webhookSender posts {"<field>": "<text>"} to a chat webhook. Slack and
// Discord differ only in that field name, so they are one implementation.
type webhookSender struct {
	name   string
	url    string
	field  string
	client *http.Client
}

func newWebhookSender(name, url, field string, client *http.Client) Sender {
	return webhookSender{name: name, url: url, field: field, client: client}
}

func (s webhookSender) Name() string { return s.name }

func (s webhookSender) Send(ctx context.Context, msg Message) error {
	text := msg.Title
	if msg.Body != "" {
		text += "\n" + msg.Body
	}
	payload, err := json.Marshal(map[string]string{s.field: text})
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", s.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s request: %w", s.name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", s.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s webhook returned %s", s.name, resp.Status)
	}
	return nil
}

// SendersFromEnv returns one sender per webhook URL present in the
// environment. A channel is active exactly when its URL is set, matching how
// ENCRYPT_KEY switches encryption on.
func SendersFromEnv() []Sender {
	client := &http.Client{Timeout: requestTimeout}
	var senders []Sender
	if url := os.Getenv("SLACK_WEBHOOK_URL"); url != "" {
		senders = append(senders, newWebhookSender("slack", url, "text", client))
	}
	if url := os.Getenv("DISCORD_WEBHOOK_URL"); url != "" {
		senders = append(senders, newWebhookSender("discord", url, "content", client))
	}
	return senders
}

// Notifier delivers enabled events to every configured sender, redacting a
// secret and truncating long bodies on the way out.
type Notifier struct {
	senders []Sender
	enabled map[Event]bool
	secret  string
}

// New builds a Notifier. events holds the raw names from the config file;
// unknown names are ignored here because Config.validate already rejects them.
// secret is removed from every body before sending.
func New(senders []Sender, events []string, secret string) *Notifier {
	enabled := make(map[Event]bool, len(events))
	for _, e := range events {
		enabled[Event(e)] = true
	}
	return &Notifier{senders: senders, enabled: enabled, secret: secret}
}

// Notify delivers msg if its event is enabled. It returns nothing: a delivery
// failure is logged and discarded, because a notification must never fail a
// backup.
func (n *Notifier) Notify(ctx context.Context, msg Message) {
	if !n.enabled[msg.Event] {
		return
	}
	msg.Body = truncate(redact(msg.Body, n.secret))
	for _, s := range n.senders {
		if err := s.Send(ctx, msg); err != nil {
			log.Printf("Warning: %s notification failed: %v", s.Name(), err)
		}
	}
}

// redact removes secret from body. This is egress to the public internet, so
// it runs even though today's error strings are not known to carry a password.
func redact(body, secret string) string {
	if secret == "" {
		return body
	}
	return strings.ReplaceAll(body, secret, "***")
}

func truncate(body string) string {
	if len(body) <= maxBody {
		return body
	}
	return body[:maxBody] + "\n… (truncated)"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./notify/ -v`
Expected: PASS, all ten tests.

- [ ] **Step 5: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add notify/notify.go notify/notify_test.go
git commit -m "feat(notify): add Slack and Discord webhook senders"
```

---

### Task 4: Subscribe to events from config

`config.yaml` gains a `notify.events` list. An unrecognised name fails at startup rather than silently swallowing an alert.

**Files:**
- Modify: `config/config.go:16-26` (the `Config` struct) and `config/config.go:45-58` (`validate`)
- Modify: `config/config_test.go`
- Modify: `config.yaml`

**Interfaces:**
- Consumes: `notify.Valid(name string) bool` from Task 3.
- Produces: `Config.Notify.Events []string`, readable as `cfg.Notify.Events`.

- [ ] **Step 1: Write the failing tests**

Append to `config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./config/ -run 'Notify' -v`
Expected: FAIL — `cfg.Notify undefined`.

- [ ] **Step 3: Write the implementation**

In `config/config.go`, add the import `"dbbackup/notify"` and this type above `Config`:

```go
// Notify selects which backup events produce a notification. An empty Events
// list disables notification entirely.
type Notify struct {
	Events []string `yaml:"events"`
}
```

Add this field to the `Config` struct, after `Cron`:

```go
	Notify           Notify              `yaml:"notify"`
```

And add this check to `validate`, before the closing `return nil`:

```go
	for _, e := range c.Notify.Events {
		if !notify.Valid(e) {
			return fmt.Errorf("notify.events contains unknown event %q", e)
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./config/ -v`
Expected: PASS, including the three pre-existing tests.

- [ ] **Step 5: Document the block in the shipped config**

In `config.yaml`, append:

```yaml
# Which events produce a Slack/Discord notification. Omit or leave empty to
# disable. Set SLACK_WEBHOOK_URL and/or DISCORD_WEBHOOK_URL to choose where
# they go.
notify:
  events:
    - backup_failed
    - upload_failed
    - remote_cleanup_failed
    # - backup_succeeded
```

- [ ] **Step 6: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add config/config.go config/config_test.go config.yaml
git commit -m "feat(config): add notify.events with startup validation"
```

---

### Task 5: Return a Report from PerformBackup

`main.go` needs to tell an upload failure from a dump failure, and needs the numbers for a success message. `PerformBackup` starts describing its run instead of only returning an error.

**Files:**
- Modify: `backup/backup.go:50-137` (the doc comment and body of `PerformBackup`, plus a new `Report` type above it)
- Modify: `backup/backup_test.go`
- Modify: `main.go:32` and `main.go:44` (the two `backup.PerformBackup` call sites)

**Interfaces:**
- Consumes: `dumpGlobals` from Task 1.
- Produces: `backup.Report` and `func PerformBackup(ctx context.Context, cfg *config.Config) (*Report, error)`. The `*Report` is never nil, including on error.

- [ ] **Step 1: Write the failing test**

Append to `backup/backup_test.go`:

```go
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
}
```

Add `"context"` and `"dbbackup/config"` to the import block of `backup/backup_test.go`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./backup/ -run 'AlwaysReturnsAReport' -v`
Expected: FAIL — `assignment mismatch: 2 variables but PerformBackup returns 1 value`.

- [ ] **Step 3: Add the Report type**

In `backup/backup.go`, insert above the `PerformBackup` doc comment:

```go
// Report describes one backup run. PerformBackup returns one even when the run
// fails, so a caller can always say what happened and how far it got.
type Report struct {
	Host      string // "host:port" of the server that was backed up
	Databases []string
	Artifact  string // path of the finished archive, "" if the run died first
	Size      int64
	Duration  time.Duration
	Stage     string // connect|globals|dump|zip|encrypt|upload|cleanup|done

	// UploadErr is also returned as the fatal error; it lives here so the
	// caller can tell an upload failure from a dump failure.
	UploadErr        error
	RemoteCleanupErr error
}
```

- [ ] **Step 4: Rewrite PerformBackup**

Replace `PerformBackup` in `backup/backup.go` with:

```go
// PerformBackup dumps the cluster globals and every non-excluded database,
// packs them into one archive, optionally encrypts and uploads it, and only
// then prunes old backups. Any failure aborts before older backups are touched.
func PerformBackup(ctx context.Context, cfg *config.Config) (*Report, error) {
	start := time.Now()
	rep := &Report{Stage: "connect"}
	defer func() { rep.Duration = time.Since(start) }()

	conn, err := pgConnFromEnv()
	if err != nil {
		return rep, err
	}
	rep.Host = conn.Host + ":" + conn.Port

	databases, err := listDatabases(ctx, conn, cfg.ExcludeDatabases)
	if err != nil {
		return rep, err
	}
	if len(databases) == 0 {
		return rep, fmt.Errorf("no databases to back up after applying exclude_databases")
	}
	rep.Databases = databases

	if err := os.MkdirAll(cfg.BackupDir, 0o755); err != nil {
		return rep, fmt.Errorf("create backup dir: %w", err)
	}
	release, err := acquireLock(cfg.BackupDir)
	if err != nil {
		return rep, err
	}
	defer release()

	stamp := time.Now().Format(timestampLayout)
	workDir := filepath.Join(cfg.BackupDir, stamp)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return rep, fmt.Errorf("create work dir: %w", err)
	}
	// The dump directory is always temporary: remove it on every exit path
	// (the happy path removes it explicitly before cleanup runs).
	defer os.RemoveAll(workDir)

	rep.Stage = "globals"
	if err := dumpGlobals(ctx, conn, workDir); err != nil {
		return rep, err
	}

	rep.Stage = "dump"
	for _, dbname := range databases {
		if err := dumpDatabase(ctx, conn, dbname, workDir, cfg.ExcludeTables[dbname]); err != nil {
			return rep, err
		}
	}

	rep.Stage = "zip"
	zipFile := workDir + ".zip"
	if err := ZipFolder(workDir, zipFile); err != nil {
		os.Remove(zipFile)
		return rep, fmt.Errorf("zip backup directory: %w", err)
	}
	if err := os.RemoveAll(workDir); err != nil {
		return rep, fmt.Errorf("remove dump directory: %w", err)
	}

	finalFile := zipFile
	if key := os.Getenv("ENCRYPT_KEY"); key != "" {
		rep.Stage = "encrypt"
		log.Printf("Encrypting %s", zipFile)
		if err := EncryptFile(zipFile, key); err != nil {
			os.Remove(zipFile)
			os.Remove(zipFile + ".gpg")
			return rep, err
		}
		finalFile = zipFile + ".gpg"
	}
	rep.Artifact = finalFile
	if info, err := os.Stat(finalFile); err == nil {
		rep.Size = info.Size()
	}

	// The local artifact is complete and verified at this point, so local
	// retention runs even if the upload below fails; the upload error is
	// still reported.
	abs := cfg.RemoteBackup.AzureBlobStorage
	if abs.Enable {
		rep.Stage = "upload"
		rep.UploadErr = UploadToABS(ctx, finalFile)
		if rep.UploadErr == nil && abs.Keep > 0 {
			if err := CleanupRemoteBackups(ctx, abs.Keep); err != nil {
				log.Printf("Warning: remote cleanup failed: %v", err)
				rep.RemoteCleanupErr = err
			}
		}
	}

	rep.Stage = "cleanup"
	if err := CleanupOldBackups(cfg.BackupDir, cfg.Keep); err != nil {
		return rep, err
	}
	if rep.UploadErr != nil {
		return rep, rep.UploadErr
	}

	rep.Stage = "done"
	log.Printf("Backup completed in %s: %s", time.Since(start).Round(time.Millisecond), finalFile)
	return rep, nil
}
```

- [ ] **Step 5: Update the two call sites in main.go**

In `main.go`, change the `--now` branch from `if err := backup.PerformBackup(ctx, cfg); err != nil {` to:

```go
		if _, err := backup.PerformBackup(ctx, cfg); err != nil {
```

And inside `c.AddFunc`, change `if err := backup.PerformBackup(ctx, cfg); err != nil {` to:

```go
		if _, err := backup.PerformBackup(ctx, cfg); err != nil {
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make test`
Expected: PASS, `go vet` clean.

- [ ] **Step 7: Commit**

```bash
git add backup/backup.go backup/backup_test.go main.go
git commit -m "refactor(backup): return a Report describing the run"
```

---

### Task 6: Send the notifications

The composition root maps a finished run onto events and posts them. The mapping lives in `main.go`, not in `notify`, so `notify` never imports `backup`.

**Files:**
- Modify: `main.go`
- Create: `main_test.go`
- Modify: `.env.example`
- Modify: `README.md`

**Interfaces:**
- Consumes: `backup.Report` and `PerformBackup(ctx, cfg) (*backup.Report, error)` from Task 5; `notify.SendersFromEnv`, `notify.New`, `notify.Notifier.Notify`, `notify.Message`, and the four `notify.Event` constants from Task 3; `cfg.Notify.Events` from Task 4.
- Produces: `eventsFor(rep *backup.Report, err error) []notify.Event` and `messageFor(ev notify.Event, rep *backup.Report, err error) notify.Message` in `package main`.

- [ ] **Step 1: Write the failing tests**

Create `main_test.go`:

```go
package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"dbbackup/backup"
	"dbbackup/notify"
)

func TestEventsForUploadFailure(t *testing.T) {
	uploadErr := errors.New("blob rejected")
	rep := &backup.Report{UploadErr: uploadErr}
	got := eventsFor(rep, uploadErr)
	if len(got) != 1 || got[0] != notify.UploadFailed {
		t.Fatalf("an upload failure must report upload_failed, got %v", got)
	}
}

func TestEventsForDumpFailure(t *testing.T) {
	rep := &backup.Report{Stage: "dump"}
	got := eventsFor(rep, errors.New("pg_dump exploded"))
	if len(got) != 1 || got[0] != notify.BackupFailed {
		t.Fatalf("expected backup_failed, got %v", got)
	}
}

func TestEventsForSuccess(t *testing.T) {
	got := eventsFor(&backup.Report{Stage: "done"}, nil)
	if len(got) != 1 || got[0] != notify.BackupSucceeded {
		t.Fatalf("expected backup_succeeded, got %v", got)
	}
}

func TestEventsForSuccessWithFailedRemoteCleanup(t *testing.T) {
	rep := &backup.Report{Stage: "done", RemoteCleanupErr: errors.New("list failed")}
	got := eventsFor(rep, nil)
	if len(got) != 2 || got[0] != notify.BackupSucceeded || got[1] != notify.RemoteCleanupFailed {
		t.Fatalf("expected success plus remote_cleanup_failed, got %v", got)
	}
}

func TestEventsForNilReport(t *testing.T) {
	got := eventsFor(nil, errors.New("boom"))
	if len(got) != 1 || got[0] != notify.BackupFailed {
		t.Fatalf("a nil report must still report a failure, got %v", got)
	}
}

func TestMessageForFailureNamesTheStage(t *testing.T) {
	rep := &backup.Report{Host: "db:5432", Stage: "cleanup"}
	msg := messageFor(notify.BackupFailed, rep, errors.New("disk full"))
	if !strings.Contains(msg.Title, "db:5432") {
		t.Fatalf("title should name the server: %q", msg.Title)
	}
	if !strings.Contains(msg.Body, "stage: cleanup") {
		t.Fatalf("body should name the stage: %q", msg.Body)
	}
	if !strings.Contains(msg.Body, "disk full") {
		t.Fatalf("body should carry the error: %q", msg.Body)
	}
}

func TestMessageForSuccessCarriesTheNumbers(t *testing.T) {
	rep := &backup.Report{
		Host:      "db:5432",
		Databases: []string{"a", "b", "c"},
		Artifact:  "/backup/20260908_000000.zip.gpg",
		Size:      24 * 1024 * 1024,
		Duration:  1900 * time.Millisecond,
	}
	msg := messageFor(notify.BackupSucceeded, rep, nil)
	for _, want := range []string{"3 databases", "24.0 MB", "1.9s", "/backup/20260908_000000.zip.gpg"} {
		if !strings.Contains(msg.Title+msg.Body, want) {
			t.Fatalf("success message is missing %q: %q / %q", want, msg.Title, msg.Body)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		512:                    "512 B",
		1536:                   "1.5 KB",
		24 * 1024 * 1024:       "24.0 MB",
		3 * 1024 * 1024 * 1024: "3.0 GB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Fatalf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -v`
Expected: FAIL — `undefined: eventsFor`, `undefined: messageFor`, `undefined: humanSize`.

- [ ] **Step 3: Write the mapping and message builders**

In `main.go`, add these functions below `main`:

```go
// eventsFor maps the outcome of a run onto the events it should report. An
// upload failure is reported as upload_failed even though it is also the
// returned error, because the local artifact is fine and the operator needs to
// know which half broke.
func eventsFor(rep *backup.Report, err error) []notify.Event {
	if rep == nil {
		return []notify.Event{notify.BackupFailed}
	}
	switch {
	case rep.UploadErr != nil:
		return []notify.Event{notify.UploadFailed}
	case err != nil:
		return []notify.Event{notify.BackupFailed}
	}
	events := []notify.Event{notify.BackupSucceeded}
	if rep.RemoteCleanupErr != nil {
		events = append(events, notify.RemoteCleanupFailed)
	}
	return events
}

func messageFor(ev notify.Event, rep *backup.Report, err error) notify.Message {
	if rep == nil {
		rep = &backup.Report{}
	}
	switch ev {
	case notify.UploadFailed:
		return notify.Message{
			Event: ev,
			Title: fmt.Sprintf("❌ db-backup upload failed — %s", rep.Host),
			Body:  fmt.Sprintf("artifact: %s\n%v", rep.Artifact, rep.UploadErr),
		}
	case notify.RemoteCleanupFailed:
		return notify.Message{
			Event: ev,
			Title: fmt.Sprintf("⚠️ db-backup remote cleanup failed — %s", rep.Host),
			Body:  fmt.Sprintf("%v", rep.RemoteCleanupErr),
		}
	case notify.BackupSucceeded:
		return notify.Message{
			Event: ev,
			Title: fmt.Sprintf("✅ db-backup ok — %s", rep.Host),
			Body: fmt.Sprintf("%d databases · %s · %s\n%s",
				len(rep.Databases), humanSize(rep.Size),
				rep.Duration.Round(100*time.Millisecond), rep.Artifact),
		}
	default:
		return notify.Message{
			Event: notify.BackupFailed,
			Title: fmt.Sprintf("❌ db-backup failed — %s", rep.Host),
			Body:  fmt.Sprintf("stage: %s\n%v", rep.Stage, err),
		}
	}
}

// humanSize renders n for a notification body.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
```

Add `"fmt"`, `"time"` and `"dbbackup/notify"` to the import block of `main.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -v`
Expected: PASS, all eight tests.

- [ ] **Step 5: Wire the notifier into main**

In `main.go`, after `cfg, err := config.ReadConfig(*configPath)` succeeds and after `ctx, stop := signal.NotifyContext(...)`, add:

```go
	senders := notify.SendersFromEnv()
	if len(cfg.Notify.Events) > 0 && len(senders) == 0 {
		log.Println("Warning: notify.events is configured but neither SLACK_WEBHOOK_URL nor DISCORD_WEBHOOK_URL is set")
	}
	notifier := notify.New(senders, cfg.Notify.Events, os.Getenv("PG_PASSWORD"))

	run := func() error {
		rep, err := backup.PerformBackup(ctx, cfg)
		// A run aborted by SIGTERM leaves ctx cancelled, which would kill the
		// webhook call too — and that run is exactly the one worth reporting.
		nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		for _, ev := range eventsFor(rep, err) {
			notifier.Notify(nctx, messageFor(ev, rep, err))
		}
		return err
	}
```

Then replace the `--now` branch body with:

```go
	if *runNow {
		log.Println("Running backup immediately...")
		if err := run(); err != nil {
			log.Printf("Backup failed: %v", err)
			os.Exit(1)
		}
		return
	}
```

And the scheduled function with:

```go
	_, err = c.AddFunc(cfg.Cron, func() {
		if err := run(); err != nil {
			log.Printf("Backup failed: %v", err)
		}
	})
```

- [ ] **Step 6: Run the full suite**

Run: `make test`
Expected: PASS, `go vet` clean.

- [ ] **Step 7: Document the environment variables**

In `.env.example`, append:

```
# Notification webhooks. Set either, both, or neither. A channel is active when
# its URL is set; which events are sent is chosen by notify.events in config.yaml.
SLACK_WEBHOOK_URL=
DISCORD_WEBHOOK_URL=
```

In `README.md`, add a `## Notifications` section immediately before `## Development`:

```markdown
## Notifications

Set `SLACK_WEBHOOK_URL`, `DISCORD_WEBHOOK_URL`, or both, and list the events you
care about under `notify.events` in `config.yaml`:

| Event | Fires when |
|---|---|
| `backup_failed` | The run failed before producing a complete archive, or local cleanup failed |
| `upload_failed` | The archive is fine locally but the upload failed |
| `remote_cleanup_failed` | The upload worked but pruning old blobs failed |
| `backup_succeeded` | The run completed |

A misspelled event name fails at startup. Delivery never fails a backup: a
webhook error is logged and the run's own result is unaffected.
```

- [ ] **Step 8: Verify end to end against a real webhook receiver**

Docker runs on `devtuf`, never locally. Create `/tmp/receiver.py` locally:

```python
import http.server

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        print(self.rfile.read(n).decode(), flush=True)
        self.send_response(200)
        self.end_headers()

http.server.HTTPServer(("0.0.0.0", 80), Handler).serve_forever()
```

Then:

```bash
rsync -az --delete --exclude '.git' --exclude 'backup_data' ./ devtuf:/tmp/dbbackup-notify/
scp /tmp/receiver.py devtuf:/tmp/dbbackup-notify/receiver.py
ssh devtuf 'cd /tmp/dbbackup-notify && docker build -q -t dbbackup:notify-test .'
ssh devtuf 'docker network create hooknet 2>/dev/null; \
  docker run -d --name hook --network hooknet -v /tmp/dbbackup-notify/receiver.py:/r.py:ro \
    python:3.12-alpine python /r.py; \
  docker run -d --name pgsrv --network hooknet -e POSTGRES_PASSWORD=secret postgres:16'
```

Wait for `pg_isready`, then run the backup with a good password and confirm
the receiver logged a JSON body containing `"text"`:

```bash
ssh devtuf 'docker run --rm --network hooknet \
  -e PG_HOST=pgsrv -e PG_PORT=5432 -e PG_USER=postgres -e PG_PASSWORD=secret -e PG_SSLMODE=disable \
  -e SLACK_WEBHOOK_URL=http://hook/ -v /tmp/out:/backup \
  -v /tmp/dbbackup-notify/config.yaml:/app/config.yaml:ro dbbackup:notify-test backup -now'
ssh devtuf 'docker logs hook'
```

Then repeat with `-e PG_PASSWORD=wrongpassword123` and confirm two things: a
failure message arrives, and `docker logs hook` does **not** contain
`wrongpassword123`. That is the redaction check, and it is the one worth doing
against a real socket rather than only in the unit test.

Clean up afterwards:

```bash
ssh devtuf 'docker rm -f hook pgsrv; docker network rm hooknet; \
  docker rmi dbbackup:notify-test; rm -rf /tmp/dbbackup-notify /tmp/out'
```

- [ ] **Step 9: Commit**

```bash
git add main.go main_test.go .env.example README.md
git commit -m "feat: report backup failures to Slack and Discord"
```

---

### Task 7: Raise the default retention

`keep: 2` on a daily cron is two days of history: corruption found on Wednesday cannot be recovered from Monday.

**Files:**
- Modify: `config.yaml`
- Modify: `config/config_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `ReadConfig` from `config`. No code changes — `CleanupOldBackups` already keeps the N newest.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Write the failing test**

Append to `config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./config/ -run 'ShippedConfig' -v`
Expected: FAIL — `shipped keep is 2; a daily cron needs at least a week of history`.

- [ ] **Step 3: Raise keep**

In `config.yaml`, change the `keep` line and its comment to:

```yaml
# Number of newest local backups to keep (must be >= 1). With a daily cron this
# is how many days of history you can restore from.
keep: 14
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./config/ -run 'ShippedConfig' -v`
Expected: PASS.

- [ ] **Step 5: Document the disk trade-off**

In `README.md`, in the section that describes `keep`, add:

```markdown
`keep` counts archives, not days, but with the default daily cron the two are
the same. It defaults to 14; raise it and disk use rises linearly, so size it
against the archive size you actually observe. Remote retention
(`remote_backup.azure_blob_storage.keep`) is counted separately, and `0` there
means unlimited.
```

- [ ] **Step 6: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add config.yaml config/config_test.go README.md
git commit -m "feat(config): keep 14 days of backups by default"
```
