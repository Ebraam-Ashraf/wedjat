package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
)

func TestConfigDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wedjat.yaml")
	if err := core.EnsureConfig(path); err != nil {
		t.Fatalf("ensure config: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if err := core.EnsureConfig(path); err != nil {
		t.Fatalf("ensure existing config: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read existing config: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("EnsureConfig modified an existing file")
	}

	loaded, err := core.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	defaults := core.DefaultConfig()
	if loaded.Storage != defaults.Storage || loaded.Polling != defaults.Polling {
		t.Fatalf("loaded config differs from defaults: got %+v, want %+v", *loaded, defaults)
	}
}

func TestLoadConfigMissingUsesDefaults(t *testing.T) {
	loaded, err := core.LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("load missing config: %v", err)
	}
	if loaded.Polling.NvmlDbTickMs != 2000 {
		t.Fatalf("unexpected default NvmlDbTickMs: got %d, want 2000", loaded.Polling.NvmlDbTickMs)
	}
	if loaded.Polling.NvmlSocketTickMs != 500 {
		t.Fatalf("unexpected default NvmlSocketTickMs: got %d, want 500", loaded.Polling.NvmlSocketTickMs)
	}
}

func TestLoadConfigMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("storage: [\nbroken:\t{{{\n"), 0644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	_, err := core.LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig with malformed YAML: expected error, got nil")
	}
}

func TestLoadConfigFieldCompleteness(t *testing.T) {
	d := core.DefaultConfig()
	if d.Storage.DayFilesDays <= 0 {
		t.Errorf("Storage.DayFilesDays = %d, want > 0", d.Storage.DayFilesDays)
	}
	if d.Storage.ProcessesDays <= 0 {
		t.Errorf("Storage.ProcessesDays = %d, want > 0", d.Storage.ProcessesDays)
	}
	if d.Storage.IncidentsDays <= 0 {
		t.Errorf("Storage.IncidentsDays = %d, want > 0", d.Storage.IncidentsDays)
	}
	if d.Polling.NvmlDbTickMs <= 0 {
		t.Errorf("Polling.NvmlDbTickMs = %d, want > 0", d.Polling.NvmlDbTickMs)
	}
	if d.Polling.NvmlSocketTickMs <= 0 {
		t.Errorf("Polling.NvmlSocketTickMs = %d, want > 0", d.Polling.NvmlSocketTickMs)
	}
	if d.Polling.NvmlSocketTickMs >= d.Polling.NvmlDbTickMs {
		t.Errorf("socket tick (%dms) must be faster than db tick (%dms)",
			d.Polling.NvmlSocketTickMs, d.Polling.NvmlDbTickMs)
	}
}

func TestValidateRejectsNegativeValues(t *testing.T) {
	cases := map[string]func(*core.Config){
		"day_files_days":      func(c *core.Config) { c.Storage.DayFilesDays = -1 },
		"processes_days":      func(c *core.Config) { c.Storage.ProcessesDays = -1 },
		"incidents_days":      func(c *core.Config) { c.Storage.IncidentsDays = -1 },
		"nvml_db_tick_ms":     func(c *core.Config) { c.Polling.NvmlDbTickMs = 0 },
		"nvml_socket_tick_ms": func(c *core.Config) { c.Polling.NvmlSocketTickMs = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := core.DefaultConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted invalid %s", name)
			}
		})
	}
}

func TestValidateAcceptsDefaults(t *testing.T) {
	cfg := core.DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config failed validation: %v", err)
	}
}
