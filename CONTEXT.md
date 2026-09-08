# db-backup

A scheduled PostgreSQL backup tool: it dumps every non-excluded database on one
server, packs the dumps into a single encrypted archive, optionally ships that
archive to any number of offsite destinations, and prunes old ones.

## Language

### The backup itself

**Backup run**:
One execution of the whole flow, from connecting to the server to pruning old
archives. Either the scheduler or `--now` starts one.
_Avoid_: job, task, execution

**Stage**:
A named step within a backup run: `connect`, `prepare`, `globals`, `dump`, `zip`,
`encrypt`, `upload`, `cleanup`, `done`. A run reports the stage it reached.
_Avoid_: step, phase

**Dump**:
The `pg_dump` custom-format file for exactly one database.
_Avoid_: backup file, export

**Globals**:
The cluster-level objects that belong to no single database — roles, their
password hashes, tablespaces, and cluster-wide grants.
_Avoid_: cluster objects, users, roles

**Backup artifact**:
The single file a successful run produces: the zip of every dump plus the
globals, encrypted when `ENCRYPT_KEY` is set. The unit that retention counts
and that gets uploaded.
_Avoid_: backup file, archive, blob

### Retention

**Local retention**:
How many of the newest backup artifacts survive in the backup directory.
_Avoid_: rotation, local keep

**Remote destination**:
One offsite place the backup artifact is shipped to — an Azure blob container or
an S3 bucket. Any number can be enabled at once, and each stores artifacts under
its own prefix, `databases/` by default.
_Avoid_: provider, backend, remote

**Remote retention**:
How many of the newest backup artifacts survive at one remote destination.
Counted separately from local retention and from every other destination.
_Avoid_: remote rotation, blob keep

### Notification

**Notification event**:
A named thing that happened during a run and that a user can subscribe to:
`backup_failed`, `upload_failed`, `remote_cleanup_failed`, `backup_succeeded`.
Subscribing is per-event.
_Avoid_: alert, hook, trigger

**Channel**:
One destination a notification is delivered to. A channel is active when its
webhook URL is present in the environment.
_Avoid_: provider, sink, integration
