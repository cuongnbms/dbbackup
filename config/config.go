package config

import (
	"fmt"
	"os"
	"path"

	"dbbackup/notify"
	"gopkg.in/yaml.v3"
)

// RemoteTarget configures one offsite destination for the backup artifact.
// Both backends carry the same two knobs, so they share this shape.
type RemoteTarget struct {
	Enable bool `yaml:"enable"`
	// Keep is the number of newest backup artifacts kept at the destination.
	// 0 means unlimited. Counted separately from local retention.
	Keep int `yaml:"keep"`
}

// RemoteBackup lists every offsite destination the artifact can be shipped to.
// Any number of them can be enabled at once.
type RemoteBackup struct {
	AzureBlobStorage RemoteTarget `yaml:"azure_blob_storage"`
	AWSS3            RemoteTarget `yaml:"aws_s3"`
}

// Notify selects which backup events produce a notification. An empty Events
// list disables notification entirely.
type Notify struct {
	Events []string `yaml:"events"`
}

type Config struct {
	// BackupDir is the local directory holding backups. Defaults to /backup.
	BackupDir string `yaml:"backup_dir"`
	// ExcludeDatabases holds glob patterns matched against every database name
	// on the server. A pattern without a metacharacter matches exactly.
	ExcludeDatabases []string `yaml:"exclude_databases"`
	// ExcludeTableData maps a database name to the pg_dump table patterns whose
	// rows are left out of its dump. The tables themselves are still dumped, so
	// views and foreign keys that depend on them still restore.
	ExcludeTableData map[string][]string `yaml:"exclude_table_data"`
	Keep             int                 `yaml:"keep"`
	Cron             string              `yaml:"cron"`
	Notify           Notify              `yaml:"notify"`
	RemoteBackup     RemoteBackup        `yaml:"remote_backup"`
}

// removedKeys are config keys that no longer exist. They are decoded separately
// from Config so the live struct carries no dead fields, and reported as an
// error: a removed key that is quietly ignored changes what ends up in the
// archive without saying so.
type removedKeys struct {
	ExcludeTables map[string][]string `yaml:"exclude_tables"`
}

func ReadConfig(filePath string) (*Config, error) {
	body, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var removed removedKeys
	if err := yaml.Unmarshal(body, &removed); err != nil {
		return nil, err
	}
	if len(removed.ExcludeTables) > 0 {
		return nil, fmt.Errorf("exclude_tables has been replaced by exclude_table_data, " +
			"which keeps the table definition and drops only its rows; rename the key")
	}

	cfg := Config{BackupDir: "/backup"}
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Keep < 1 {
		return fmt.Errorf("keep must be >= 1 (got %d), otherwise every backup would be deleted", c.Keep)
	}
	if c.Cron == "" {
		return fmt.Errorf("cron must be set")
	}
	if c.BackupDir == "" {
		return fmt.Errorf("backup_dir must not be empty")
	}
	// A malformed pattern silently matches nothing, which would back up a
	// database the operator believes is excluded.
	for _, pattern := range c.ExcludeDatabases {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("exclude_databases pattern %q is malformed: %w", pattern, err)
		}
	}
	if c.RemoteBackup.AzureBlobStorage.Keep < 0 {
		return fmt.Errorf("remote_backup.azure_blob_storage.keep must be >= 0")
	}
	if c.RemoteBackup.AWSS3.Keep < 0 {
		return fmt.Errorf("remote_backup.aws_s3.keep must be >= 0")
	}
	for _, e := range c.Notify.Events {
		if !notify.Valid(e) {
			return fmt.Errorf("notify.events contains unknown event %q", e)
		}
	}
	return nil
}
