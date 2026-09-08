package backup

import "testing"

func TestAcquireLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(dir); err == nil {
		t.Fatal("second lock must fail while first is held")
	}
	release()
	release2, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("lock must be reacquirable after release: %v", err)
	}
	release2()
}
