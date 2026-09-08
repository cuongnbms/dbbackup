package backup

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// globalsArgs builds the pg_dumpall argv. With noPasswords the dump omits role
// password hashes, which needs no privileged read of pg_authid.
func globalsArgs(conn pgConn, outFile string, noPasswords bool) []string {
	args := []string{"-h", conn.Host, "-p", conn.Port, "-U", conn.User, "--globals-only", "-f", outFile}
	if noPasswords {
		args = append(args, "--no-role-passwords")
	}
	return args
}

// dumpGlobals writes the cluster-level objects that no per-database pg_dump
// contains: roles, their password hashes, tablespaces and cluster-wide grants.
// Without them a restore into an empty cluster fails on every OWNER TO and
// GRANT naming a role that was never created.
//
// On any failure it retries once without role passwords. The retry is
// unconditional rather than gated on recognising a permission error, because
// pg_dumpall's message varies by version and locale and matching it would rot
// silently into "never retry". If the retry also fails the first error is
// returned: it is the informative one.
func dumpGlobals(ctx context.Context, conn pgConn, workDir string) error {
	outFile := filepath.Join(workDir, "globals.sql")

	log.Println("Backing up cluster globals")
	if firstErr := runPgDumpall(ctx, conn, outFile, false); firstErr != nil {
		if err := runPgDumpall(ctx, conn, outFile, true); err != nil {
			return firstErr
		}
		log.Printf("Warning: globals dumped without role passwords: %v", firstErr)
	}

	// There is no pg_restore --list equivalent for a plain SQL file, so the
	// check is that the file is not empty.
	info, err := os.Stat(outFile)
	if err != nil {
		return fmt.Errorf("stat globals dump: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("globals dump is empty: %s", outFile)
	}
	log.Println("Backup completed for cluster globals")
	return nil
}

func runPgDumpall(ctx context.Context, conn pgConn, outFile string, noPasswords bool) error {
	cmd := exec.CommandContext(ctx, "pg_dumpall", globalsArgs(conn, outFile, noPasswords)...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+conn.Password, "PGSSLMODE="+conn.SSLMode)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_dumpall --globals-only: %w: %s", err, output)
	}
	return nil
}
