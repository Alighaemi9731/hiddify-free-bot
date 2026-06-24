package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds runtime configuration loaded from the environment / config.env.
type Config struct {
	BotToken string
	AdminID  int64
	DataDir  string // directory that stores the sqlite db + backups
}

// Load reads configuration from environment variables. The installer writes
// these into /etc/hidybot/config.env which systemd injects via EnvironmentFile.
func Load() (*Config, error) {
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return nil, errors.New("BOT_TOKEN is required")
	}

	adminRaw := strings.TrimSpace(os.Getenv("ADMIN_ID"))
	if adminRaw == "" {
		return nil, errors.New("ADMIN_ID is required")
	}
	adminID, err := strconv.ParseInt(adminRaw, 10, 64)
	if err != nil {
		return nil, errors.New("ADMIN_ID must be a numeric Telegram user id")
	}

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/var/lib/hidybot"
	}

	return &Config{
		BotToken: token,
		AdminID:  adminID,
		DataDir:  dataDir,
	}, nil
}

// DBPath returns the absolute path to the sqlite database file.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "hidybot.db") }

// BackupDir returns the directory used to store local backup snapshots.
func (c *Config) BackupDir() string { return filepath.Join(c.DataDir, "backups") }
