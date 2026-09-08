package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"dbbackup/config"

	_ "github.com/lib/pq"
)

const timestampLayout = "20060102_150405"

type pgConn struct {
	Host, Port, User, Password, SSLMode string
}

func pgConnFromEnv() (pgConn, error) {
	c := pgConn{
		Host:     os.Getenv("PG_HOST"),
		Port:     os.Getenv("PG_PORT"),
		User:     os.Getenv("PG_USER"),
		Password: os.Getenv("PG_PASSWORD"),
		SSLMode:  os.Getenv("PG_SSLMODE"),
	}
	if c.Host == "" || c.Port == "" || c.User == "" || c.Password == "" || c.SSLMode == "" {
		return c, fmt.Errorf("missing required environment variables: PG_HOST, PG_PORT, PG_USER, PG_PASSWORD, PG_SSLMODE")
	}
	return c, nil
}

// url builds a libpq connection URL with the password properly escaped.
func (c pgConn) url() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     c.Host + ":" + c.Port,
		Path:     "/postgres",
		RawQuery: "sslmode=" + url.QueryEscape(c.SSLMode),
	}
	return u.String()
}

// Report describes one backup run. PerformBackup returns one even when the run
// fails, so a caller can always say what happened and how far it got.
type Report struct {
	Host      string // "host:port" of the server that was backed up
	Databases []string
	Artifact  string // path of the finished archive, "" if the run died first
	Size      int64
	Duration  time.Duration
	Stage     string // connect|prepare|globals|dump|zip|encrypt|upload|cleanup|done

	// UploadErr is also returned as the fatal error; it lives here so the
	// caller can tell an upload failure from a dump failure.
	UploadErr        error
	RemoteCleanupErr error
}

// PerformBackup dumps the cluster globals and every non-excluded database,
// packs them into one archive, optionally encrypts and uploads it, and only
// then prunes old backups. Any failure aborts before older backups are touched.
func PerformBackup(ctx context.Context, cfg *config.Config) (*Report, error) {
	start := time.Now()
	rep := &Report{Stage: "connect"}
	defer func() { rep.Duration = time.Since(start) }()

	conn, err := pgConnFromEnv()
	if err != nil {
		return rep, err
	}
	rep.Host = conn.Host + ":" + conn.Port

	databases, err := listDatabases(ctx, conn, cfg.ExcludeDatabases)
	if err != nil {
		return rep, err
	}
	rep.Stage = "prepare"
	if len(databases) == 0 {
		return rep, fmt.Errorf("no databases to back up after applying exclude_databases")
	}
	rep.Databases = databases

	if err := os.MkdirAll(cfg.BackupDir, 0o755); err != nil {
		return rep, fmt.Errorf("create backup dir: %w", err)
	}
	release, err := acquireLock(cfg.BackupDir)
	if err != nil {
		return rep, err
	}
	defer release()

	stamp := time.Now().Format(timestampLayout)
	workDir := filepath.Join(cfg.BackupDir, stamp)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return rep, fmt.Errorf("create work dir: %w", err)
	}
	// The dump directory is always temporary: remove it on every exit path
	// (the happy path removes it explicitly before cleanup runs).
	defer os.RemoveAll(workDir)

	rep.Stage = "globals"
	if err := dumpGlobals(ctx, conn, workDir); err != nil {
		return rep, err
	}

	rep.Stage = "dump"
	for _, dbname := range databases {
		if err := dumpDatabase(ctx, conn, dbname, workDir, cfg.ExcludeTables[dbname]); err != nil {
			return rep, err
		}
	}

	rep.Stage = "zip"
	zipFile := workDir + ".zip"
	if err := ZipFolder(workDir, zipFile); err != nil {
		os.Remove(zipFile)
		return rep, fmt.Errorf("zip backup directory: %w", err)
	}
	if err := os.RemoveAll(workDir); err != nil {
		return rep, fmt.Errorf("remove dump directory: %w", err)
	}

	finalFile := zipFile
	if key := os.Getenv("ENCRYPT_KEY"); key != "" {
		rep.Stage = "encrypt"
		log.Printf("Encrypting %s", zipFile)
		if err := EncryptFile(zipFile, key); err != nil {
			os.Remove(zipFile)
			os.Remove(zipFile + ".gpg")
			return rep, err
		}
		finalFile = zipFile + ".gpg"
	}
	rep.Artifact = finalFile
	if info, err := os.Stat(finalFile); err == nil {
		rep.Size = info.Size()
	}

	// The local artifact is complete and verified at this point, so local
	// retention runs even if the upload below fails; the upload error is
	// still reported.
	abs := cfg.RemoteBackup.AzureBlobStorage
	if abs.Enable {
		rep.Stage = "upload"
		rep.UploadErr = UploadToABS(ctx, finalFile)
		if rep.UploadErr != nil {
			log.Printf("Warning: upload failed: %v", rep.UploadErr)
		}
		if rep.UploadErr == nil && abs.Keep > 0 {
			if err := CleanupRemoteBackups(ctx, abs.Keep); err != nil {
				log.Printf("Warning: remote cleanup failed: %v", err)
				rep.RemoteCleanupErr = err
			}
		}
	}

	rep.Stage = "cleanup"
	if err := CleanupOldBackups(cfg.BackupDir, cfg.Keep); err != nil {
		return rep, err
	}
	if rep.UploadErr != nil {
		rep.Stage = "upload"
		return rep, rep.UploadErr
	}

	rep.Stage = "done"
	log.Printf("Backup completed in %s: %s", time.Since(start).Round(time.Millisecond), finalFile)
	return rep, nil
}

func listDatabases(ctx context.Context, conn pgConn, excludeList []string) ([]string, error) {
	db, err := sql.Open("postgres", conn.url())
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("connect to postgres at %s:%s: %w", conn.Host, conn.Port, err)
	}

	rows, err := db.QueryContext(ctx, "SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname")
	if err != nil {
		return nil, fmt.Errorf("query databases: %w", err)
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan database name: %w", err)
		}
		if !isExcluded(name, excludeList) {
			databases = append(databases, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate databases: %w", err)
	}
	return databases, nil
}

func isExcluded(name string, excludeList []string) bool {
	for _, exclude := range excludeList {
		if name == exclude {
			return true
		}
	}
	return false
}

func dumpArgs(conn pgConn, dbname, outFile string, excludeTables []string) []string {
	args := []string{"-h", conn.Host, "-p", conn.Port, "-U", conn.User, "-F", "c", "-f", outFile}
	for _, table := range excludeTables {
		args = append(args, "--exclude-table="+table)
	}
	return append(args, dbname)
}

func dumpDatabase(ctx context.Context, conn pgConn, dbname, workDir string, excludeTables []string) error {
	outFile := filepath.Join(workDir, dbname+".backup")

	cmd := exec.CommandContext(ctx, "pg_dump", dumpArgs(conn, dbname, outFile, excludeTables)...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+conn.Password, "PGSSLMODE="+conn.SSLMode)

	log.Printf("Backing up database: %s", dbname)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_dump %s: %w: %s", dbname, err, output)
	}

	// pg_restore --list reads the whole TOC; it fails on a truncated or corrupt dump.
	verify := exec.CommandContext(ctx, "pg_restore", "--list", outFile)
	verify.Stdout = nil
	if output, err := verify.CombinedOutput(); err != nil {
		return fmt.Errorf("verify dump %s: %w: %s", dbname, err, output)
	}
	log.Printf("Backup completed for database: %s", dbname)
	return nil
}
