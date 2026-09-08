package backup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Only files this tool produced are ever deleted; anything else in the
// backup directory is left alone.
var (
	backupFileRe = regexp.MustCompile(`^\d{8}_\d{6}\.zip(\.gpg)?$`)
	backupDirRe  = regexp.MustCompile(`^\d{8}_\d{6}$`)
)

// CleanupOldBackups keeps the keepCount newest backup archives and deletes the
// rest. Leftover dump directories from an interrupted run are removed too.
func CleanupOldBackups(backupDir string, keepCount int) error {
	if keepCount < 1 {
		return fmt.Errorf("keep must be >= 1, got %d", keepCount)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return fmt.Errorf("list backup dir: %w", err)
	}

	var archives []string
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(backupDir, name)
		switch {
		case e.IsDir() && backupDirRe.MatchString(name):
			log.Printf("Removing leftover dump directory: %s", path)
			if err := os.RemoveAll(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			}
		case e.Type().IsRegular() && backupFileRe.MatchString(name):
			archives = append(archives, name)
		}
	}

	// Names start with a timestamp, so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(archives)))

	for _, name := range archives[min(keepCount, len(archives)):] {
		path := filepath.Join(backupDir, name)
		log.Printf("Deleting old backup: %s", path)
		if err := os.Remove(path); err != nil {
			log.Printf("Failed to delete %s: %v", path, err)
		}
	}
	return nil
}
