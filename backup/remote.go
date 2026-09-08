package backup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"sort"
	"time"

	"dbbackup/config"
)

// remotePrefix is the key prefix every backup artifact lives under, in every
// remote destination. Anything else at the destination is left alone.
const remotePrefix = "databases/"

// remoteKey is where an artifact lands at a destination. Every backend uses
// the same key, so an operator reading one destination can find the matching
// object in the other.
func remoteKey(filePath string) string {
	return remotePrefix + filepath.Base(filePath)
}

// remoteTarget is one offsite destination for the backup artifact.
type remoteTarget interface {
	// Name identifies the destination in logs and notifications.
	Name() string
	// Upload stores filePath under remotePrefix.
	Upload(ctx context.Context, filePath string) error
	// Cleanup deletes all but the keepCount newest artifacts under
	// remotePrefix.
	Cleanup(ctx context.Context, keepCount int) error
}

// remoteDestination pairs a target with its own remote retention. Retention is
// per destination: a bucket kept for a month and a container kept for a week
// are a normal setup, not a misconfiguration.
type remoteDestination struct {
	target remoteTarget
	keep   int
}

// uploadDeadlineFloor is the least time any destination gets, however small the
// artifact. Even a few hundred kilobytes costs a DNS lookup, a TLS handshake
// and a multipart create before the first byte of payload moves, so a deadline
// derived purely from the size would fail a healthy upload.
const uploadDeadlineFloor = 5 * time.Minute

// uploadDeadline returns how long one destination gets to accept an artifact of
// size bytes, given the slowest transfer rate still worth waiting for in KiB/s.
// A rate of 0 returns 0, meaning no deadline at all.
//
// Deriving the budget from the size rather than fixing it means a cluster that
// grows does not silently walk into a deadline that was generous when it was
// set: the artifact and its budget grow together.
func uploadDeadline(size int64, minKBps int) time.Duration {
	if minKBps <= 0 {
		return 0
	}
	d := time.Duration(float64(size) / float64(int64(minKBps)*1024) * float64(time.Second))
	if d < uploadDeadlineFloor {
		return uploadDeadlineFloor
	}
	return d
}

// uploadWithin gives one destination its own slice of time. The budget is per
// destination and not shared: a stalled S3 must not spend the time Azure needs,
// for the same reason a failed S3 does not stop the Azure upload.
//
// An expired budget is reported as a timeout. Neither SDK caps a request on its
// own -- the AWS transport sets no response-header or client timeout, and
// azcore leaves TryTimeout at 0 -- so without this the run hangs on a wedged
// connection until TCP keepalive gives up, and cron then skips every following
// run with nothing but a log line to say why.
func uploadWithin(ctx context.Context, target remoteTarget, filePath string, deadline time.Duration) error {
	if deadline <= 0 {
		return target.Upload(ctx, filePath)
	}
	uctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	err := target.Upload(uctx, filePath)
	// A run killed by SIGTERM arrives here with the same error class as a
	// destination that ran out of budget, and is not one: only the derived
	// context expiring while the run itself is still alive is a timeout.
	if err != nil && ctx.Err() == nil && uctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s: %w", deadline, err)
	}
	return err
}

// shipToRemotes uploads filePath to every destination, then prunes the ones
// that accepted it. Each upload gets deadline to finish in; 0 means it gets as
// long as the run itself.
//
// A destination that fails never stops the others: the whole point of a second
// destination is that it survives the first one being down. A destination whose
// upload failed is not pruned either, because its newest artifact is missing
// and pruning to keep N would leave only N-1 good backups there.
//
// The returned errors are joined across destinations and kept apart from each
// other, because they mean different things to the operator: a failed upload
// means the offsite copy does not exist, a failed prune means it does but old
// ones are piling up.
func shipToRemotes(ctx context.Context, dests []remoteDestination, filePath string, deadline time.Duration) (uploadErr, cleanupErr error) {
	var uploadErrs, cleanupErrs []error
	for _, dest := range dests {
		name := dest.target.Name()
		if err := uploadWithin(ctx, dest.target, filePath, deadline); err != nil {
			log.Printf("Warning: upload to %s failed: %v", name, err)
			uploadErrs = append(uploadErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if dest.keep <= 0 {
			continue
		}
		if err := dest.target.Cleanup(ctx, dest.keep); err != nil {
			log.Printf("Warning: remote cleanup on %s failed: %v", name, err)
			cleanupErrs = append(cleanupErrs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(uploadErrs...), errors.Join(cleanupErrs...)
}

// remoteDestinations builds a destination for every enabled backend.
//
// A backend whose credentials are missing yields an error rather than a
// destination, and the ones that did build are still returned: a broken S3
// setup must not also cost the run its Azure copy.
func remoteDestinations(rb config.RemoteBackup) ([]remoteDestination, error) {
	var (
		dests []remoteDestination
		errs  []error
	)
	add := func(cfg config.RemoteTarget, build func() (remoteTarget, error), name string) {
		if !cfg.Enable {
			return
		}
		target, err := build()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return
		}
		dests = append(dests, remoteDestination{target: target, keep: cfg.Keep})
	}
	add(rb.AzureBlobStorage, func() (remoteTarget, error) { return absFromEnv() }, "Azure Blob Storage")
	add(rb.AWSS3, func() (remoteTarget, error) { return s3FromEnv() }, "AWS S3")
	return dests, errors.Join(errs...)
}

// archivesToDelete returns the backup artifacts that fall outside the keepCount
// newest. Names that do not look like a backup artifact are never returned, so
// anything else stored under the same prefix survives. keepCount <= 0 means
// keep everything.
//
// The timestamp layout sorts lexically in chronological order, which is why
// sorting the names is the same as sorting by age.
func archivesToDelete(names []string, keepCount int) []string {
	if keepCount <= 0 {
		return nil
	}
	var archives []string
	for _, name := range names {
		if backupFileRe.MatchString(path.Base(name)) {
			archives = append(archives, name)
		}
	}
	if len(archives) <= keepCount {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(archives)))
	return archives[keepCount:]
}
