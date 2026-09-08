# 0002: Fail the whole backup when globals cannot be dumped, with no opt-out

> Status: Accepted · Date: 2026-09-08

## Context

`pg_dump` of an individual database captures nothing cluster-level. Measured on
a test cluster: a per-database custom-format archive contains 0 role entries,
while `pg_dumpall --globals-only` on the same cluster emits 2 `CREATE ROLE`
lines. Restoring such an archive into an empty cluster fails on every
`ALTER ... OWNER TO` and `GRANT` naming a role that was never created.

So `pg_dumpall --globals-only` joins the flow. The question is what happens
when it fails — most plausibly because `PG_USER` cannot read `pg_authid` on a
managed PostgreSQL service.

Source: [design spec](../superpowers/specs/2026-09-08-backup-hardening-design.md)

## Decision

A failed globals dump fails the backup run. There is no configuration flag to
skip globals or to downgrade the failure to a warning.

A backup that reports success while being unrestorable is the exact failure
this work exists to remove; an opt-out would reintroduce it as a supported
setting.

The predictable cause is softened rather than made configurable: any failure
triggers one retry with `--no-role-passwords`, which needs no privileged read.
Only if that also fails does the run fail.

## Consequences

An environment that blocks `pg_dumpall` outright has no escape hatch and will
see every run fail until the grant is fixed. This is judged low-risk — on
Azure Database for PostgreSQL Flexible Server the admin role can run
`pg_dumpall --globals-only` — but it is a real edge with no workaround short of
editing the code.

`globals.sql` contains SCRAM password hashes. With `ENCRYPT_KEY` unset they sit
in plaintext inside the artifact and in blob storage. The README warns; the
tool does not force encryption on, because that would be a second mandatory
policy bolted onto this one.

## Alternatives considered

| Alternative | Why not chosen |
|-------------|----------------|
| `globals.enable` config flag | The only reason to turn it off is to make a known-incomplete backup report success. |
| Log a warning and continue when globals fail | Indistinguishable from a healthy run in the notification and the logs, which is how the original gap survived. |
| Always pass `--no-role-passwords` | Restored roles cannot authenticate; every password would have to be reset by hand mid-incident. |
| Detect the permission error by matching `pg_dumpall`'s output | The message varies by version and locale, so the detection would rot silently into "never retry". |
