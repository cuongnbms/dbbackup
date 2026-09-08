package backup

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestZipFolderStoresDumpsAndDeflatesText(t *testing.T) {
	src := t.TempDir()
	// Incompressible bytes, so a deflate attempt cannot be mistaken for a store.
	blob := make([]byte, 4096)
	for i := range blob {
		blob[i] = byte(i * 7919 % 251)
	}
	if err := os.WriteFile(filepath.Join(src, "demo.backup"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "globals.sql"), []byte(strings.Repeat("CREATE ROLE app;\n", 200)), 0o644); err != nil {
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

	methods := map[string]uint16{}
	for _, f := range r.File {
		methods[f.Name] = f.Method
	}
	if methods["demo.backup"] != zip.Store {
		t.Fatalf("demo.backup should be stored, got method %d", methods["demo.backup"])
	}
	if methods["globals.sql"] != zip.Deflate {
		t.Fatalf("globals.sql should be deflated, got method %d", methods["globals.sql"])
	}
}

func TestZipFolderPreservesModTime(t *testing.T) {
	src := t.TempDir()
	path := filepath.Join(src, "a.backup")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, want, want); err != nil {
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

	// Zip timestamps have two-second granularity.
	if delta := r.File[0].Modified.Sub(want); delta > 2*time.Second || delta < -2*time.Second {
		t.Fatalf("modtime not preserved: got %v, want %v", r.File[0].Modified, want)
	}
}
