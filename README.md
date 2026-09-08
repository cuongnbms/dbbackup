# db-backup

Scheduled PostgreSQL backups: `pg_dump` every database, verify each dump with
`pg_restore --list`, pack into one zip, optionally encrypt with GPG and upload
to Azure Blob Storage, then keep only the newest N backups.

A failed dump aborts the run before anything is uploaded or deleted, so a bad
run never removes an older good backup.

## Configuration

`config.yaml` (mounted at `/app/config.yaml`):

```yaml
keep: 2              # newest local backups to keep, must be >= 1
cron: 0 0 * * *      # 5-field cron
# backup_dir: /backup

remote_backup:
  azure_blob_storage:
    enable: false
    keep: 0          # newest blobs to keep in the container, 0 = unlimited

# exclude_databases:
#   - postgres
# exclude_tables:
#   demo:
#     - users
```

Environment variables (see `.env.example`):

```
PG_HOST, PG_PORT, PG_USER, PG_PASSWORD, PG_SSLMODE   required
ENCRYPT_KEY        optional, GPG symmetric passphrase; output is <ts>.zip.gpg
ABS_ACCOUNT_NAME   Azure storage account name        } required when
ABS_ACCESS_KEY     Azure storage account access key  } azure_blob_storage.enable
ABS_CONTAINER      Azure blob container              } is true
```

Backups are written to `/backup/<YYYYMMDD_HHMMSS>.zip[.gpg]` locally and to
`databases/<YYYYMMDD_HHMMSS>.zip[.gpg]` in the Azure container.

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
pg_restore -h HOST -U USER -d DBNAME --clean --if-exists 20240101_000000/DBNAME.backup
```

## Development

```sh
make test
```

The runtime image pins `postgresql17-client`; `pg_dump` must be at least as new
as the server, so bump the package in the `Dockerfile` if your server is newer.
