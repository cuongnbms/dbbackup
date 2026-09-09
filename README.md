# db-backup

Scheduled PostgreSQL backups: `pg_dump` every database, verify each dump with
`pg_restore --list`, pack into one zip, optionally encrypt with GPG and upload
offsite to Azure Blob Storage and/or AWS S3, then keep only the newest N backups.

A failed dump aborts the run before anything is uploaded or deleted, so a bad
run never removes an older good backup.

## Configuration

`config.yaml` (mounted at `/app/config.yaml`):

```yaml
keep: 14             # newest local backups to keep, must be >= 1
cron: 0 0 * * *      # 5-field cron
# backup_dir: /backup

remote_backup:
  azure_blob_storage:
    enable: false
    keep: 0          # newest artifacts to keep at this destination, 0 = unlimited
  aws_s3:
    enable: false
    keep: 0
  # per-destination upload deadline = artifact size / this rate, min 5 minutes.
  # 0 = no deadline (a wedged connection then hangs the run).
  min_upload_speed_kbps: 1024

# exclude_databases:
#   - postgres
#   - "test_*"
# exclude_table_data:
#   demo:
#     - public.django_session
#     - public.log_*
```

### Exclusions

`exclude_databases` skips whole databases. Each entry is a glob pattern matched
against the database name — `*`, `?` and `[0-9]` all work, and a pattern with no
metacharacter matches exactly, so a plain list of names behaves as it reads.
Template databases are always skipped. A malformed pattern fails at startup
rather than silently matching nothing.

`exclude_table_data` drops the *rows* of the listed tables while still dumping
the tables themselves. That distinction matters: a dump missing a table still
contains every view and foreign key that references it, and since `pg_restore
--list` only reads the archive's table of contents, such a dump verifies
cleanly and then fails at restore time, possibly months later. Keeping the
definitions means the restore stays consistent; only the data is gone.

```yaml
exclude_table_data:
  demo:
    - public.django_session
    - public.log_*
```

Entries are pg_dump table patterns, so wildcards work. Two rules to know:

- **Qualify with a schema.** An unqualified pattern only matches tables visible
  in the connection's `search_path`, so `users` finds `public.users` but not
  `audit.users`. Write `audit.users`, or `*.users` for every schema.
- **Partitions are included.** An entry naming a partitioned table also empties
  its partitions and inheritance children, which is the usual reason to exclude
  a table's data in the first place.

Excluding a table's data is safe for leaf tables — logs, sessions, caches. If
rows in another table reference the emptied one, creating that foreign key
fails on restore.

`keep` counts archives, not days, but with the default daily cron the two are
the same. The shipped `config.yaml` sets it to 14; raise it and disk use rises
linearly, so size it against the archive size you actually observe.

Both destinations can be enabled at once. Each carries its own retention, counted
separately from local retention and from each other, and `0` means unlimited. A
destination that fails does not stop the other one: the run still uploads to
whichever is reachable and reports the failure.

Environment variables (see `.env.example`):

```
PG_HOST, PG_PORT, PG_USER, PG_PASSWORD, PG_SSLMODE   required
ENCRYPT_KEY        optional, GPG symmetric passphrase; output is <ts>.zip.gpg

ABS_ACCOUNT_NAME   Azure storage account name        } required when
ABS_ACCESS_KEY     Azure storage account access key  } azure_blob_storage.enable
ABS_CONTAINER      Azure blob container              } is true

S3_BUCKET          target bucket                     } required when
AWS_REGION         bucket region                     } aws_s3.enable is true
S3_ENDPOINT        optional, for S3-compatible stores (MinIO, Ceph, R2)
```

S3 credentials come from the standard AWS chain — `AWS_ACCESS_KEY_ID` /
`AWS_SECRET_ACCESS_KEY`, a mounted `~/.aws`, or an EC2/ECS/EKS instance role — so
running on a role needs no keys in `.env` at all. `S3_ENDPOINT` also switches the
client to path-style addressing, which is what S3-compatible stores expect; leave
it empty for real AWS.

Backups are written to `/backup/<YYYYMMDD_HHMMSS>.zip[.gpg]` locally and to
`<prefix><YYYYMMDD_HHMMSS>.zip[.gpg]` at every enabled remote destination. The
prefix is per destination — `remote_backup.<destination>.prefix` in
`config.yaml`, `databases/` by default — so one bucket shared with other things
can be given a folder of its own. Anything under the prefix that does not look
like a backup artifact is never deleted by remote retention, and the prefix
itself may not be empty: at the root of a bucket, retention would reach
artifacts belonging to whatever else is stored there.

## Run

```sh
cp .env.example .env   # fill in real values
docker compose up -d
```

Run a backup immediately (exit code is non-zero on failure; refuses to start
if the scheduled job is already running):

```sh
docker exec -it dbbackup backup --now
```

## Restore

```sh
gpg --decrypt 20240101_000000.zip.gpg > 20240101_000000.zip   # if encrypted
unzip 20240101_000000.zip -d 20240101_000000   # contains one DBNAME.backup per database
psql -h HOST -U USER -d postgres -f 20240101_000000/globals.sql
pg_restore -h HOST -U USER -d DBNAME --clean --if-exists 20240101_000000/DBNAME.backup
```

Each archive also contains `globals.sql` — the cluster's roles, tablespaces and
grants, which no per-database `pg_dump` captures. Restore it first, before any
`pg_restore`, or every `OWNER TO` and `GRANT` will fail.

`globals.sql` contains SCRAM password hashes. With `ENCRYPT_KEY` unset those
hashes sit in plaintext inside the archive and in blob storage. If the backup
user cannot read `pg_authid` the dump falls back to `--no-role-passwords`
automatically and logs a warning; restored roles then need their passwords set
by hand.

## Notifications

Set `SLACK_WEBHOOK_URL`, `DISCORD_WEBHOOK_URL`, or both, and list the events you
care about under `notify.events` in `config.yaml`:

| Event | Fires when |
|---|---|
| `backup_failed` | The run failed before producing a complete archive, or local cleanup failed |
| `upload_failed` | The archive is fine locally but at least one remote upload failed |
| `remote_cleanup_failed` | The upload worked but pruning old remote artifacts failed |
| `backup_succeeded` | The run completed |

A misspelled event name fails at startup. Delivery never fails a backup: a
webhook error is logged and the run's own result is unaffected.

When several servers post to the same channel, set `INSTANCE_NAME` to tell them
apart. The title otherwise names only `PG_HOST:PG_PORT`, which is `db:5432` on
every server that reaches Postgres through a compose service of that name:

```
❌ [prod-hanoi-01] db-backup failed — db:5432
✅ [prod-hanoi-01] db-backup ok — db:5432
```

Leave it unset on a single server and the titles stay as they were.

## Development

```sh
make test
```

The runtime image pins `postgresql18-client` (Alpine 3.24); `pg_dump` must be at
least as new as the server (18 can dump servers back to 9.2), so bump the package
and the base image in the `Dockerfile` if your server is newer.

Dumps are custom-format archives. pg_dump 18 writes archive version 1.16, which
needs `pg_restore` 17 or newer to read; restore with the client from this image
(or any 17+ client), whatever major version the target server is. Restoring into a
server older than 17 logs one ignorable `transaction_timeout` error, so avoid
`-1` / `--exit-on-error` there.

## Release

Images are published to [`cuongnb14/db-backup`](https://hub.docker.com/r/cuongnb14/db-backup)
on Docker Hub, for `linux/amd64` and `linux/arm64`.

Publishing a GitHub release with tag `vX.Y` builds and pushes
`cuongnb14/db-backup:X.Y`. A full release also moves `:latest`; a prerelease
does not, so `:latest` always points at a real release. `.github/workflows/release.yml`
also accepts a manual run (`workflow_dispatch`) taking a tag — useful for
rebuilding one version, and it never touches `:latest`.

The workflow needs two repository secrets: `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` (a Docker Hub access token, not the account password).

The version in `Makefile` and in `docker-compose.yml` is not updated by the
release; bump it by hand in the commit you tag.
