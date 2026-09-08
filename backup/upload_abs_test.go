package backup

import "testing"

func TestBlobName(t *testing.T) {
	if got := blobName("/backup/20240101_000000.zip.gpg"); got != "databases/20240101_000000.zip.gpg" {
		t.Fatalf("got %s", got)
	}
}

func TestBlobsToDelete(t *testing.T) {
	names := []string{
		"databases/20240103_000000.zip.gpg",
		"databases/20240101_000000.zip.gpg",
		"databases/20240102_000000.zip",
		"databases/manual-export.sql", // foreign blob: never deleted
	}
	got := blobsToDelete(names, 2)
	if len(got) != 1 || got[0] != "databases/20240101_000000.zip.gpg" {
		t.Fatalf("got %v", got)
	}
	if got := blobsToDelete(names, 0); len(got) != 0 {
		t.Fatalf("keep=0 must mean unlimited, got %v", got)
	}
}
