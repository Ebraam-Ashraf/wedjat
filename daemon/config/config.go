package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultDataPath = "/var/lib/wedjat"
	DefaultLockPath = "/run/wedjat/daemon.lock"
)

type Config struct {
	Storage   Storage   `yaml:"storage"`
	Retention Retention `yaml:"retention"`
}

type Storage struct {
	Path         string `yaml:"path"`
	LockPath     string `yaml:"lock_path"`
	ResetOnBoot  bool   `yaml:"reset_on_boot"`
	MaxSizeBytes int64  `yaml:"max_size_bytes"`
	MinFreeBytes int64  `yaml:"min_free_bytes"`
}

type Retention struct {
	DayFilesDays  int `yaml:"day_files_days"`
	ProcessesDays int `yaml:"processes_days"`
	IncidentsDays int `yaml:"incidents_days"`
	MaxDumps      int `yaml:"max_dumps"`
}

func Default() Config {
	return Config{
		Storage: Storage{
			Path:         DefaultDataPath,
			LockPath:     DefaultLockPath,
			ResetOnBoot:  true,
			MaxSizeBytes: 500 << 20,
			MinFreeBytes: 1 << 30,
		},
		Retention: Retention{
			DayFilesDays:  30,
			ProcessesDays: 90,
			IncidentsDays: 90,
			MaxDumps:      20,
		},
	}
}

func (c Config) Validate() error {
	if err := ValidateDataPath(c.Storage.Path); err != nil {
		return fmt.Errorf("storage.path: %w", err)
	}
	if !filepath.IsAbs(c.Storage.LockPath) || filepath.Clean(c.Storage.LockPath) != c.Storage.LockPath {
		return errors.New("storage.lock_path must be a clean absolute path")
	}
	if isWithin(c.Storage.Path, c.Storage.LockPath) {
		return errors.New("storage.lock_path must be outside the data directory")
	}
	if c.Storage.MaxSizeBytes <= 0 {
		return errors.New("storage.max_size_bytes must be positive")
	}
	if c.Storage.MinFreeBytes < 0 {
		return errors.New("storage.min_free_bytes must not be negative")
	}
	if c.Retention.DayFilesDays <= 0 || c.Retention.ProcessesDays <= 0 ||
		c.Retention.IncidentsDays <= 0 || c.Retention.MaxDumps <= 0 {
		return errors.New("all retention limits must be positive")
	}
	return nil
}

func ValidateDataPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("must be a clean absolute path")
	}
	if path == string(filepath.Separator) {
		return errors.New("filesystem root cannot be the data directory")
	}
	for _, home := range []string{"/home", "/root"} {
		if isWithin(home, path) {
			return errors.New("home directories cannot be used for Wedjat data")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && isWithin(home, path) {
		return errors.New("user home cannot be used for Wedjat data")
	}
	return nil
}

func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
