package core

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds daemon configuration loaded from YAML.
type Config struct {
	Storage   StorageConfig   `yaml:"storage"`
	Retention RetentionConfig `yaml:"retention"`
	Tracing   TracingConfig   `yaml:"tracing"`
}

type StorageConfig struct {
	ResetOnBoot  bool  `yaml:"reset_on_boot"`
	MaxSizeBytes int64 `yaml:"max_size_bytes"`
	MinFreeBytes int64 `yaml:"min_free_bytes"`
}

type RetentionConfig struct {
	DayFilesDays  int `yaml:"day_files_days"`
	ProcessesDays int `yaml:"processes_days"`
	IncidentsDays int `yaml:"incidents_days"`
	MaxDumps      int `yaml:"max_dumps"`
}

// TracingConfig controls the eBPF side. Tracing is on by default but degrades to
// NVML-only if the kernel refuses the load, since a machine without the right
// privileges should still report GPU telemetry.
type TracingConfig struct {
	// Enabled turns the tracer off entirely. NVML polling is unaffected.
	Enabled bool `yaml:"enabled"`
	// RawCapture sends every event to userspace instead of only counting the
	// hot paths. It makes process exit and sync latency exact, at a real cost
	// in ring buffer traffic.
	RawCapture bool `yaml:"raw_capture"`
	// SyncStallUs is how long one sync may take before it is recorded as a
	// stall. Zero means the default.
	SyncStallUs uint32 `yaml:"sync_stall_us"`
	// ObjectsDir holds the compiled BPF objects. Empty means the default.
	ObjectsDir string `yaml:"objects_dir"`
	// PinDir holds the pinned state maps. Empty means the default.
	PinDir string `yaml:"pin_dir"`
	// LibcudaPath overrides CUDA library discovery, which is needed on
	// distributions that install the driver somewhere unusual.
	LibcudaPath string `yaml:"libcuda_path"`
	// FixLibcudaPermissions sets the execute bit on the CUDA driver library when
	// it is missing. eBPF uprobes are matched by inode, so tracing the CUDA API
	// requires the real library to be readable as executable; distributions ship
	// it 0644 and the package manager restores that on every driver upgrade.
	//
	// This is on by default because the alternative is a daemon that reports
	// nothing while appearing healthy, and the bit is not a security boundary
	// for a library already mapped executable in every process that loads it.
	// Every application is logged, and it can be set false to leave the library
	// untouched.
	FixLibcudaPermissions bool `yaml:"fix_libcuda_permissions"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Storage: StorageConfig{
			ResetOnBoot:  true,
			MaxSizeBytes: 500 << 20, // 500 MB
			MinFreeBytes: 1 << 30,   // 1 GB
		},
		Retention: RetentionConfig{
			DayFilesDays:  30,
			ProcessesDays: 90,
			IncidentsDays: 90,
			MaxDumps:      20,
		},
		Tracing: TracingConfig{
			Enabled:               true,
			FixLibcudaPermissions: true,
		},
	}
}

// LoadConfig reads the YAML config file and returns a Config.
// If the file doesn't exist, returns default config.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			return &cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}

// EnsureConfig writes the default config file at path if it does not already
// exist, creating the parent directory when needed. An existing file is never
// modified, so operator edits survive restarts and upgrades.
func EnsureConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}

	data, err := yaml.Marshal(DefaultConfig())
	if err != nil {
		return fmt.Errorf("encode default config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write default config: %w", err)
	}
	return nil
}
