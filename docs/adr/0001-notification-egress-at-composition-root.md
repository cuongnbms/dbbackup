# 0001: Keep notification egress out of the backup package, at the cost of a wider PerformBackup signature

> Status: Accepted · Date: 2026-09-08

## Context

Backups fail silently: in scheduler mode a failed run logs a line and the
process carries on, so a broken nightly backup can go unnoticed indefinitely.
Adding Slack and Discord notifications means something has to decide which
event a finished run represents and post it to the internet.

The obvious place is inside `PerformBackup`, where the failures happen. That
would put an HTTP client, webhook URLs, and "is this event enabled" checks into
the middle of the dump-zip-encrypt-upload flow.

Source: [design spec](../superpowers/specs/2026-09-08-backup-hardening-design.md)

## Decision

`PerformBackup` returns a `*Report` describing what happened — stage reached,
databases, artifact, size, duration, upload error, remote-cleanup error — and
never talks to a notifier. `main.go` maps that `Report` plus the returned error
onto notification events and calls `notify`.

This keeps `notify` free of any import of `backup`, and makes the
report-to-event mapping a pure function that tests without a network, which
injecting a `Notifier` into `PerformBackup` does not.

## Consequences

`PerformBackup`'s signature changes from `error` to `(*Report, error)`, so both
call sites in `main.go` and the existing tests change with it.

Two things that were previously `log.Printf` warnings buried inside
`PerformBackup` — the remote-cleanup failure in particular — now have to be
carried out on the `Report` instead. That is the point, but it means the
struct grows a field every time a new non-fatal failure becomes worth
reporting.

## Alternatives considered

| Alternative | Why not chosen |
|-------------|----------------|
| Inject a `Notifier` interface into `PerformBackup` and call it at each failure site | Couples the backup flow to network egress and forces every `PerformBackup` test to build a stub. |
| Generic webhook with a user-supplied JSON body template | A user could not get a Slack message without first writing a template; only Slack and Discord were asked for. |
