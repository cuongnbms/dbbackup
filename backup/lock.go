package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock takes an exclusive, non-blocking flock on <dir>/.backup.lock so
// that a `backup --now` started from another process cannot run concurrently
// with the scheduler (cron's SkipIfStillRunning only covers one process).
func acquireLock(dir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".backup.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another backup is already running (lock %s held)", f.Name())
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
