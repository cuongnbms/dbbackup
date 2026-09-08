package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dbbackup/config"
)

// fakeTarget records what shipToRemotes asked it to do.
type fakeTarget struct {
	name        string
	uploadErr   error
	cleanupErr  error
	uploaded    []string
	cleanupKeep []int
}

func (f *fakeTarget) Name() string { return f.name }

func (f *fakeTarget) Upload(_ context.Context, filePath string) error {
	f.uploaded = append(f.uploaded, filePath)
	return f.uploadErr
}

func (f *fakeTarget) Cleanup(_ context.Context, keepCount int) error {
	f.cleanupKeep = append(f.cleanupKeep, keepCount)
	return f.cleanupErr
}

func TestShipToRemotesUploadsAndPrunesEveryDestination(t *testing.T) {
	a := &fakeTarget{name: "A"}
	b := &fakeTarget{name: "B"}
	dests := []remoteDestination{{target: a, keep: 3}, {target: b, keep: 7}}

	uploadErr, cleanupErr := shipToRemotes(context.Background(), dests, "/backup/x.zip")

	if uploadErr != nil || cleanupErr != nil {
		t.Fatalf("expected no errors, got upload=%v cleanup=%v", uploadErr, cleanupErr)
	}
	if len(a.uploaded) != 1 || a.uploaded[0] != "/backup/x.zip" {
		t.Fatalf("A did not receive the artifact: %v", a.uploaded)
	}
	if len(a.cleanupKeep) != 1 || a.cleanupKeep[0] != 3 {
		t.Fatalf("A pruned with the wrong keep: %v", a.cleanupKeep)
	}
	if len(b.cleanupKeep) != 1 || b.cleanupKeep[0] != 7 {
		t.Fatalf("B pruned with the wrong keep: %v", b.cleanupKeep)
	}
}

// A destination whose upload failed must not be pruned: its newest artifact is
// missing, so pruning to keep N would leave N-1 good backups there.
func TestShipToRemotesDoesNotPruneAfterAFailedUpload(t *testing.T) {
	a := &fakeTarget{name: "A", uploadErr: errors.New("bucket rejected")}
	dests := []remoteDestination{{target: a, keep: 3}}

	uploadErr, cleanupErr := shipToRemotes(context.Background(), dests, "/backup/x.zip")

	if uploadErr == nil {
		t.Fatal("expected the upload error to be reported")
	}
	if cleanupErr != nil {
		t.Fatalf("a skipped prune is not a cleanup failure: %v", cleanupErr)
	}
	if len(a.cleanupKeep) != 0 {
		t.Fatalf("A must not have been pruned: %v", a.cleanupKeep)
	}
}

// One broken destination must not cost the backup its other offsite copy.
func TestShipToRemotesKeepsGoingAfterOneDestinationFails(t *testing.T) {
	a := &fakeTarget{name: "A", uploadErr: errors.New("bucket rejected")}
	b := &fakeTarget{name: "B"}
	dests := []remoteDestination{{target: a, keep: 3}, {target: b, keep: 3}}

	uploadErr, _ := shipToRemotes(context.Background(), dests, "/backup/x.zip")

	if uploadErr == nil {
		t.Fatal("expected A's failure to be reported")
	}
	if len(b.uploaded) != 1 {
		t.Fatalf("B should still have received the artifact: %v", b.uploaded)
	}
	if len(b.cleanupKeep) != 1 {
		t.Fatalf("B should still have been pruned: %v", b.cleanupKeep)
	}
}

// The joined error must name both destinations, otherwise the notification
// says "upload failed" without saying which half of the offsite copy is gone.
func TestShipToRemotesJoinsErrorsFromEveryDestination(t *testing.T) {
	aErr := errors.New("bucket rejected")
	bErr := errors.New("container missing")
	dests := []remoteDestination{
		{target: &fakeTarget{name: "A", uploadErr: aErr}, keep: 3},
		{target: &fakeTarget{name: "B", uploadErr: bErr}, keep: 3},
	}

	uploadErr, _ := shipToRemotes(context.Background(), dests, "/backup/x.zip")

	if !errors.Is(uploadErr, aErr) || !errors.Is(uploadErr, bErr) {
		t.Fatalf("both causes must survive the join: %v", uploadErr)
	}
	for _, want := range []string{"A", "B"} {
		if !strings.Contains(uploadErr.Error(), want) {
			t.Fatalf("error should name destination %s: %v", want, uploadErr)
		}
	}
}

func TestShipToRemotesReportsCleanupFailureSeparately(t *testing.T) {
	cleanErr := errors.New("list failed")
	dests := []remoteDestination{{target: &fakeTarget{name: "A", cleanupErr: cleanErr}, keep: 3}}

	uploadErr, cleanupErr := shipToRemotes(context.Background(), dests, "/backup/x.zip")

	if uploadErr != nil {
		t.Fatalf("the upload succeeded: %v", uploadErr)
	}
	if !errors.Is(cleanupErr, cleanErr) {
		t.Fatalf("expected the cleanup error, got %v", cleanupErr)
	}
}

// keep 0 means unlimited remote retention, so there is nothing to list or
// delete and the destination must not be called at all.
func TestShipToRemotesSkipsPruneWhenKeepIsUnlimited(t *testing.T) {
	a := &fakeTarget{name: "A"}
	dests := []remoteDestination{{target: a, keep: 0}}

	if _, cleanupErr := shipToRemotes(context.Background(), dests, "/backup/x.zip"); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if len(a.cleanupKeep) != 0 {
		t.Fatalf("keep 0 must not prune: %v", a.cleanupKeep)
	}
}

func TestRemoteKey(t *testing.T) {
	if got := remoteKey("/backup/20240101_000000.zip.gpg"); got != "databases/20240101_000000.zip.gpg" {
		t.Fatalf("got %s", got)
	}
}

func TestArchivesToDelete(t *testing.T) {
	names := []string{
		"databases/20240103_000000.zip.gpg",
		"databases/20240101_000000.zip.gpg",
		"databases/20240102_000000.zip",
		"databases/manual-export.sql", // foreign blob: never deleted
	}
	got := archivesToDelete(names, 2)
	if len(got) != 1 || got[0] != "databases/20240101_000000.zip.gpg" {
		t.Fatalf("got %v", got)
	}
	if got := archivesToDelete(names, 0); len(got) != 0 {
		t.Fatalf("keep=0 must mean unlimited, got %v", got)
	}
}

// enableABS supplies credentials good enough to build a client; nothing in
// these tests talks to Azure.
func enableABS(t *testing.T) {
	t.Helper()
	t.Setenv("ABS_ACCOUNT_NAME", "acct")
	t.Setenv("ABS_ACCESS_KEY", "dGVzdA==")
	t.Setenv("ABS_CONTAINER", "backups")
}

func TestRemoteDestinationsBuildsOnlyEnabledTargets(t *testing.T) {
	isolateAWSConfig(t)
	enableABS(t)
	var rb config.Config
	rb.RemoteBackup.AzureBlobStorage = config.RemoteTarget{Enable: true, Keep: 4}

	dests, err := remoteDestinations(rb.RemoteBackup)
	if err != nil {
		t.Fatal(err)
	}
	if len(dests) != 1 || dests[0].keep != 4 {
		t.Fatalf("expected only the Azure destination with keep 4, got %+v", dests)
	}
}

func TestRemoteDestinationsIsEmptyWhenNothingIsEnabled(t *testing.T) {
	var rb config.Config
	dests, err := remoteDestinations(rb.RemoteBackup)
	if err != nil || len(dests) != 0 {
		t.Fatalf("got %+v, %v", dests, err)
	}
}

// A destination missing its credentials must not cost the other one its copy:
// the working destination is still returned, alongside the error.
func TestRemoteDestinationsReportsABrokenTargetWithoutDroppingTheOthers(t *testing.T) {
	isolateAWSConfig(t) // leaves S3_BUCKET unset
	enableABS(t)
	var rb config.Config
	rb.RemoteBackup.AzureBlobStorage = config.RemoteTarget{Enable: true, Keep: 4}
	rb.RemoteBackup.AWSS3 = config.RemoteTarget{Enable: true, Keep: 4}

	dests, err := remoteDestinations(rb.RemoteBackup)
	if err == nil || !strings.Contains(err.Error(), "S3_BUCKET") {
		t.Fatalf("expected an error naming the missing S3 variable, got %v", err)
	}
	if len(dests) != 1 {
		t.Fatalf("the Azure destination should have survived, got %+v", dests)
	}
}
