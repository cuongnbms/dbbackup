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

// TestEventsForUploadFailureFollowedByCleanupFailure covers the compound case:
// PerformBackup runs local cleanup after a failed upload and returns the
// cleanup error, leaving rep.UploadErr set. The fatal error is the one that
// must be reported, otherwise the local cleanup failure is never mentioned
// anywhere in the notification.
func TestEventsForUploadFailureFollowedByCleanupFailure(t *testing.T) {
	rep := &backup.Report{
		Stage:     "cleanup",
		UploadErr: errors.New("blob rejected"),
	}
	got := eventsFor(rep, errors.New("remove old backup: permission denied"))
	if len(got) != 1 || got[0] != notify.BackupFailed {
		t.Fatalf("a cleanup failure after a failed upload must report backup_failed, got %v", got)
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

// TestEventsForFailureWithFailedRemoteCleanup covers a remote prune that failed
// before the run went on to die in local cleanup. remote_cleanup_failed is
// about the blobs, not about how the run ended, so it must still be reported
// alongside the fatal failure.
func TestEventsForFailureWithFailedRemoteCleanup(t *testing.T) {
	rep := &backup.Report{
		Stage:            "cleanup",
		RemoteCleanupErr: errors.New("list failed"),
	}
	got := eventsFor(rep, errors.New("remove old backup: permission denied"))
	if len(got) != 2 || got[0] != notify.BackupFailed || got[1] != notify.RemoteCleanupFailed {
		t.Fatalf("expected backup_failed plus remote_cleanup_failed, got %v", got)
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
	// notify.Notifier redacts and truncates Body but never Title, so error
	// text must never reach the Title.
	if strings.Contains(msg.Title, "disk full") {
		t.Fatalf("error text must stay out of the unredacted title: %q", msg.Title)
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
