package config

import (
	"fmt"
	"os"

	"dbbackup/notify"
	"gopkg.in/yaml.v3"
)

type AzureBlobStorage struct {
	Enable bool `yaml:"enable"`
	// Keep is the number of newest backups kept in the container. 0 means unlimited.
	Keep int `yaml:"keep"`
}

// Notify selects which backup events produce a notification. An empty Events
// list disables notification entirely.
type Notify struct {
	Events []string `yaml:"events"`
}

type Config struct {
	// BackupDir is the local directory holding backups. Defaults to /backup.
	BackupDir        string              `yaml:"backup_dir"`
	ExcludeDatabases []string            `yaml:"exclude_databases"`
	ExcludeTables    map[string][]string `yaml:"exclude_tables"`
	Keep             int                 `yaml:"keep"`
	Cron             string              `yaml:"cron"`
	Notify           Notify              `yaml:"notify"`
	RemoteBackup     struct {
		AzureBlobStorage AzureBlobStorage `yaml:"azure_blob_storage"`
	} `yaml:"remote_backup"`
}

func ReadConfig(filePath string) (*Config, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	cfg := Config{BackupDir: "/backup"}
	if err := yaml.NewDecoder(file).Decode(&cfg); err != nil {
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
	if c.RemoteBackup.AzureBlobStorage.Keep < 0 {
		return fmt.Errorf("remote_backup.azure_blob_storage.keep must be >= 0")
	}
	for _, e := range c.Notify.Events {
		if !notify.Valid(e) {
			return fmt.Errorf("notify.events contains unknown event %q", e)
		}
	}
	return nil
}
