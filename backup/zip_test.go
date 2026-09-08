package backup

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestZipFolderRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.backup"), []byte("hello"), 0o644); err != nil {
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
	if len(r.File) != 1 || r.File[0].Name != "a.backup" {
		t.Fatalf("unexpected zip contents: %+v", r.File)
	}
}

func TestZipFolderMissingSource(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.zip")
	if err := ZipFolder("/nonexistent/dir", target); err == nil {
		t.Fatal("expected error")
	}
}
