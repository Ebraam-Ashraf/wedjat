package core

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds daemon configuration loaded from YAML.
type Config struct {
	Storage StorageConfig `yaml:"storage"`
	Polling PollingConfig `yaml:"polling"`
	Tracing TracingConfig `yaml:"tracing"`
}

// StorageConfig controls persistence: what to keep and for how long.
type StorageConfig struct {
	DayFilesDays  int `yaml:"day_files_days"`
	ProcessesDays int `yaml:"processes_days"`
	IncidentsDays int `yaml:"incidents_days"`
}

// PollingConfig controls how often the NVML hardware is sampled and how
// often the eBPF kernel counters are drained.
type PollingConfig struct {
	// NvmlDbTickMs is the NVML database output interval (ms). When clients are
	// connected, polling may be faster, but DB messages stay at this cadence.
	NvmlDbTickMs int `yaml:"nvml_db_tick_ms"`
	// NvmlSocketTickMs is the NVML socket output interval (ms) while at least one
	// client is connected. Polling runs as fast as needed to meet both output
	// cadences; the dispatcher gates DB and socket messages independently.
	NvmlSocketTickMs int `yaml:"nvml_socket_tick_ms"`
	// EbpfDrainTickMs is the interval (ms) at which eBPF aggregate maps are
	// drained into the database. The kernel buffers counts in per-CPU maps;
	// this tick determines how often they are folded into process/device rows.
	// Zero means the default (1000ms).
	EbpfDrainTickMs int `yaml:"ebpf_drain_tick_ms"`
}

// TracingConfig controls eBPF tracing behavior.
type TracingConfig struct {
	// Enabled turns eBPF tracing off entirely. NVML polling is unaffected.
	Enabled bool `yaml:"enabled"`
	// RawCapture sends every event to userspace instead of only counting the
	// hot paths. This makes process exit and sync latency exact, at the cost
	// of noticeably more ring buffer traffic.
	RawCapture bool `yaml:"raw_capture"`
	// SyncStallUs is how long a single CUDA sync may take before it is
	// recorded as a stall. Zero uses the built-in default of 250000 (250 ms).
	SyncStallUs uint32 `yaml:"sync_stall_us"`
	// ObjectsDir holds the compiled BPF objects. Empty means the default.
	ObjectsDir string `yaml:"objects_dir"`
	// PinDir holds the pinned state maps. Empty means the default.
	PinDir string `yaml:"pin_dir"`
	// LibcudaPath overrides CUDA library discovery.
	LibcudaPath string `yaml:"libcuda_path"`
	// LibcudartPath overrides CUDA runtime library discovery.
	LibcudartPath string `yaml:"libcudart_path"`
	// FixLibcudaPermissions sets the execute bit on the CUDA driver library
	// when it is missing. eBPF uprobes are matched by inode, so tracing the
	// CUDA API requires the real library to be readable as executable.
	FixLibcudaPermissions bool `yaml:"fix_libcuda_permissions"`
	// FixLibcudartPermissions sets the execute bit on the CUDA runtime library
	// when it is missing.
	FixLibcudartPermissions bool `yaml:"fix_libcudart_permissions"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Storage: StorageConfig{
			DayFilesDays:  30,
			ProcessesDays: 90,
			IncidentsDays: 90,
		},
		Polling: PollingConfig{
			NvmlDbTickMs:     2000,
			NvmlSocketTickMs: 500,
			EbpfDrainTickMs:  1000,
		},
		Tracing: TracingConfig{
			Enabled:                 true,
			FixLibcudaPermissions:   true,
			FixLibcudartPermissions: true,
		},
	}
}

// Validate checks that all config values are within acceptable ranges.
func (c *Config) Validate() error {
	if c.Storage.DayFilesDays < 0 {
		return fmt.Errorf("storage.day_files_days must not be negative")
	}
	if c.Storage.ProcessesDays < 0 {
		return fmt.Errorf("storage.processes_days must not be negative")
	}
	if c.Storage.IncidentsDays < 0 {
		return fmt.Errorf("storage.incidents_days must not be negative")
	}
	if c.Polling.NvmlDbTickMs <= 0 {
		return fmt.Errorf("polling.nvml_db_tick_ms must be greater than zero")
	}
	if c.Polling.NvmlSocketTickMs <= 0 {
		return fmt.Errorf("polling.nvml_socket_tick_ms must be greater than zero")
	}
	if c.Polling.EbpfDrainTickMs < 0 {
		return fmt.Errorf("polling.ebpf_drain_tick_ms must not be negative")
	}
	return nil
}

// LoadConfig reads the YAML config file at path.
// If the file does not exist, the default config is returned.
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
// exist. An existing file is never modified.
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
