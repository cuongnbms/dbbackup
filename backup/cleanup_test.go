package backup

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func touch(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupOldBackupsKeepsNewestAndIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	touch(t, filepath.Join(dir, "20240101_000000.zip.gpg"), base)
	touch(t, filepath.Join(dir, "20240102_000000.zip.gpg"), base.Add(time.Minute))
	touch(t, filepath.Join(dir, "20240103_000000.zip"), base.Add(2*time.Minute))
	touch(t, filepath.Join(dir, "notes.txt"), base)
	if err := os.Mkdir(filepath.Join(dir, "20231231_000000"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, "20231231_000000", "app.backup"), base)

	if err := CleanupOldBackups(dir, 2); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{"20240102_000000.zip.gpg", "20240103_000000.zip", "notes.txt"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

func TestCleanupOldBackupsRejectsZeroKeep(t *testing.T) {
	if err := CleanupOldBackups(t.TempDir(), 0); err == nil {
		t.Fatal("expected error for keep < 1")
	}
}
