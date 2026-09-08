# Backup hardening: globals, notifications, retention, zip method

> Date: 2026-09-08 · Status: Approved, not yet implemented

## Why

A comparison against [prodrigestivill/docker-postgres-backup-local](https://github.com/prodrigestivill/docker-postgres-backup-local)
surfaced four gaps, ranked by how badly each one hurts:

1. **Globals are never backed up.** Verified empirically: a per-database
   `pg_dump` archive contains **0** role entries, while
   `pg_dumpall --globals-only` on the same cluster emits 2 `CREATE ROLE`
   lines. Restoring into a fresh cluster therefore fails on
   `ALTER ... OWNER TO` and `GRANT` for roles that do not exist. A backup that
   cannot be restored into a fresh cluster is not a backup.
2. **Failures are silent.** In scheduler mode `main.go` logs
   `Backup failed: %v` and continues. The container stays healthy to Docker
   forever while every nightly run fails.
3. **Retention is two days.** `keep: 2` on a daily cron means corruption
   discovered on Wednesday cannot be recovered from Monday.
4. **The zip re-compresses already-compressed dumps** for an 8% gain.

Measured on a 101 MB table (PostgreSQL 18, on devtuf):

| | size | time |
|---|---|---|
| `pg_dump -Fc` (default compression) | 24M | 1.90s |
| ↳ then deflated again by `ZipFolder` | 22M | |
| `pg_dump -Fc -Z 0` | 105M | 0.62s |
| ↳ then deflated once | 24M | |

The current order is *faster* than dumping raw and letting zip do the work,
because zip only has to deflate 24M instead of 105M. So item 4 is a micro-optimisation, not a defect, and it is scoped accordingly.

## Scope

In: globals dump, Slack + Discord notifications, a longer default retention,
per-file zip method.

Out: GFS retention tiers (see Alternatives), generic webhook templating,
any change to the ABS upload path beyond reporting its failure distinctly.

## Design

### Configuration surface

`config.yaml` gains one block:

```yaml
notify:
  events:
    - backup_failed
    - upload_failed
    - remote_cleanup_failed
    # - backup_succeeded
```

`.env` gains two variables:

```
SLACK_WEBHOOK_URL=
DISCORD_WEBHOOK_URL=
```

Rules:

- A **channel** is active when its environment variable is non-empty. This
  matches the existing `ENCRYPT_KEY` convention: secrets live in the
  environment, policy lives in YAML. Webhook URLs are secrets — anyone holding
  one can post into the channel.
- An absent or empty `notify.events` disables notification entirely. Existing
  `config.yaml` files keep working untouched.
- An unrecognised event name fails `Config.validate()` at startup, alongside
  the existing `keep` / `cron` / `backup_dir` checks. A typo must not silently
  swallow an alert.
- Events configured with no channel URL present logs a warning at startup. Not
  fatal.

`keep` in the shipped `config.yaml` goes from `2` to `14`. No code change:
`CleanupOldBackups` already keeps the N newest.

### Event boundaries

| Event | Condition |
|---|---|
| `backup_failed` | Failure before a complete artifact exists (list databases, globals, dump, zip, encrypt), **or** local cleanup failed |
| `upload_failed` | Artifact is complete locally, ABS upload failed |
| `remote_cleanup_failed` | Upload succeeded, pruning remote blobs failed |
| `backup_succeeded` | The run completed |

A local-cleanup failure means the artifact is actually fine, yet it maps to
`backup_failed` because `PerformBackup` already returns an error there and this
design does not change that semantics. The message carries `stage: cleanup` so
the reader can tell the two apart without a fifth event.

### `backup.Report`

`PerformBackup` becomes `(*Report, error)`:

```go
type Report struct {
    Host      string        // PG host, so the message names the server
    Databases []string
    Artifact  string        // "" when the run died before producing one
    Size      int64
    Duration  time.Duration
    Stage     string        // globals|dump|zip|encrypt|upload|cleanup|done
    UploadErr        error
    RemoteCleanupErr error
}
```

The fatal error stays the return value rather than a `Report` field, so there
is one source of truth for "did this run fail". The upload error is still
returned as the fatal error exactly as today — `--now` still exits 1 — and
`Report.UploadErr` exists only so the event can be classified as
`upload_failed` instead of `backup_failed`.

`RemoteCleanupErr` replaces a `log.Printf` warning currently buried inside
`PerformBackup`.

Mapping, a pure function living in `main.go`:

```
UploadErr != nil   -> upload_failed
else err != nil    -> backup_failed
else               -> backup_succeeded (+ remote_cleanup_failed if set)
```

It lives at the composition root, not in `notify`, so that `notify` never
imports `backup` and the two packages stay independent.

### `package notify`

```go
type Event string   // backup_failed | upload_failed | remote_cleanup_failed | backup_succeeded
type Message struct { Event Event; Title, Body string }
type Sender interface { Send(context.Context, Message) error }
```

Slack and Discord differ only in the JSON field name — `{"text": ...}` versus
`{"content": ...}` — so both are one `webhookSender` parameterised by that
name, not two implementations. `SendersFromEnv()` returns the active ones.

Three rules that are not negotiable:

1. **Notification never fails a backup.** `http.Client{Timeout: 10s}`, no
   retry, errors are logged as warnings and discarded.
2. **Bodies truncate at ~1500 characters.** Discord's hard limit is 2000, and
   a failed `pg_dump` returns its whole `CombinedOutput()`, which can exceed
   that and lose the entire alert.
3. **The password is redacted.** Every occurrence of `conn.Password` in a body
   is replaced with `***` before sending. The risk is low today, but this is
   egress to the public internet.

Message shape:

```
❌ db-backup failed — db:5432
stage: dump
pg_dump demo: exit status 3: could not connect to server
```

```
✅ db-backup ok — db:5432
3 databases · 24 MB · 1.9s
/backup/20260908_000000.zip.gpg
```

### Globals

New `backup/globals.go`, running **before** the per-database dump loop:

```
pg_dumpall -h HOST -p PORT -U USER --globals-only -f <workDir>/globals.sql
```

with the same `PGPASSWORD` / `PGSSLMODE` environment as `dumpDatabase`.

It runs first so that a permission problem fails the run in a second rather
than after a twenty-minute dump.

**Fallback:** on any failure, retry once with `--no-role-passwords`. The retry
is unconditional rather than gated on recognising a permission error, because
`pg_dumpall`'s error text varies by version and locale and matching it is a
bug waiting to happen. If the retry also fails the run fails, reporting the
**first** error — the informative one.

**Verification:** there is no `pg_restore --list` equivalent for a plain SQL
file, so the check is that `globals.sql` is non-empty. An empty globals file is
exactly the kind of silent hole this work exists to close.

Two accepted costs:

- A globals failure fails the whole run, with no opt-out flag. See
  [ADR 0002](../../adr/0002-globals-dump-mandatory.md).
- `globals.sql` contains SCRAM password hashes. Without `ENCRYPT_KEY` they sit
  in plaintext inside the artifact and on ABS. README warns; the tool does not
  force encryption on.

### Zip method

`ZipFolder` picks the method per entry instead of deflating everything:

- `.backup` (custom format, already zlib-compressed) → `zip.Store`
- everything else (`.sql`) → `zip.Deflate`

This drops a deflate pass over 24 MB of already-compressed data in exchange for
about 8% of size, and keeps `globals.sql` — plain text — compressed.

Switching from `archive.Create` to `archive.CreateHeader` also fixes an existing
cosmetic bug: entries currently carry no modification time and extract as
`1980-01-01`. The header sets `Modified: info.ModTime()`.

## Testing

Standard library only, PATH shims for external commands, `t.Setenv`, no mocking
framework — matching `backup/dump_test.go`.

| Area | Tests |
|---|---|
| `config` | `notify.events` parses; unknown event fails validation; empty disables |
| Mapping | table test over the four branches, plus `remote_cleanup_failed` alongside success |
| `notify` | `httptest.Server` captures the body and asserts Slack `{"text":…}` vs Discord `{"content":…}`; truncation at 1500; password redaction; a sender error does not escape; a sleeping handler proves the timeout |
| globals | `globalsArgs()` argv assertion mirroring `TestDumpArgs…`; a `pg_dumpall` shim that fails first and succeeds with `--no-role-passwords` proves the fallback; empty file is an error |
| zip | the `.backup` entry has `Method == zip.Store`, the `.sql` entry has `Deflate`, round-trip still readable |

Beyond `make test`: an end-to-end run on **devtuf** (Docker runs there, per
`~/.claude/CLAUDE.md`) against a throwaway webhook receiver, to confirm real
payloads arrive.

## Files

New: `notify/notify.go`, `notify/notify_test.go`, `backup/globals.go`,
`backup/globals_test.go`.

Changed: `config/config.go`, `config/config_test.go`, `backup/backup.go`,
`backup/backup_test.go`, `backup/zip.go`, `backup/zip_test.go`, `main.go`,
`config.yaml`, `.env.example`, `README.md`.

## Alternatives considered

| Alternative | Why not chosen |
|---|---|
| GFS retention tiers (last/daily/weekly/monthly by hardlink), as prodrigestivill does | Hardlinks do not exist in blob storage, so the remote side would need real duplicate uploads or would lose the tiers, and the file naming and `cleanup.go` both get restructured. Raising `keep` buys most of the history for none of that. |
| Thinning retention on the flat directory (N newest + weekly + monthly, one pure function shared by local and remote) | Rejected in favour of simply raising `keep`; revisit if disk becomes the binding constraint. |
| Inject a `Notifier` into `PerformBackup` and call it at each failure site | Couples `backup` to network egress, forces a stub to test `PerformBackup`, and interleaves "is this event enabled" with backup logic. |
| Generic webhook plus a user-supplied JSON template | Users would have to write a template before Slack works at all. Only Slack and Discord were asked for. |
| Match `pg_dumpall`'s error text to decide whether to retry with `--no-role-passwords` | The text varies by version and locale; an unconditional single retry is simpler and cannot silently stop working. |
| Always pass `--no-role-passwords` | Restored roles cannot authenticate, so every password must be reset by hand during an incident. |
