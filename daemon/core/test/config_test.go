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
	if loaded.Storage != defaults.Storage || loaded.Retention != defaults.Retention || loaded.Tracing != defaults.Tracing {
		t.Fatalf("loaded config differs from defaults: got %+v, want %+v", *loaded, defaults)
	}
}

func TestLoadConfigMissingUsesDefaults(t *testing.T) {
	loaded, err := core.LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("load missing config: %v", err)
	}
	if loaded.Storage.MaxSizeBytes != 500<<20 || !loaded.Tracing.Enabled {
		t.Fatalf("unexpected missing-file defaults: %+v", *loaded)
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
	if d.Storage.MaxSizeBytes <= 0 {
		t.Errorf("Storage.MaxSizeBytes = %d, want > 0", d.Storage.MaxSizeBytes)
	}
	if d.Storage.MinFreeBytes <= 0 {
		t.Errorf("Storage.MinFreeBytes = %d, want > 0", d.Storage.MinFreeBytes)
	}
	if !d.Storage.ResetOnBoot {
		t.Error("Storage.ResetOnBoot = false, want true")
	}
	if d.Retention.DayFilesDays <= 0 {
		t.Errorf("Retention.DayFilesDays = %d, want > 0", d.Retention.DayFilesDays)
	}
	if d.Retention.ProcessesDays <= 0 {
		t.Errorf("Retention.ProcessesDays = %d, want > 0", d.Retention.ProcessesDays)
	}
	if d.Retention.IncidentsDays <= 0 {
		t.Errorf("Retention.IncidentsDays = %d, want > 0", d.Retention.IncidentsDays)
	}
	if d.Retention.MaxDumps <= 0 {
		t.Errorf("Retention.MaxDumps = %d, want > 0", d.Retention.MaxDumps)
	}
	if !d.Tracing.Enabled {
		t.Error("Tracing.Enabled = false, want true")
	}
	if !d.Tracing.FixLibcudaPermissions {
		t.Error("Tracing.FixLibcudaPermissions = false, want true")
	}
}

func TestValidateRejectsNegativeValues(t *testing.T) {
	cases := map[string]func(*core.Config){
		"max_size_bytes": func(c *core.Config) { c.Storage.MaxSizeBytes = -1 },
		"min_free_bytes": func(c *core.Config) { c.Storage.MinFreeBytes = -1 },
		"day_files_days": func(c *core.Config) { c.Retention.DayFilesDays = -1 },
		"processes_days": func(c *core.Config) { c.Retention.ProcessesDays = -1 },
		"incidents_days": func(c *core.Config) { c.Retention.IncidentsDays = -1 },
		"max_dumps":      func(c *core.Config) { c.Retention.MaxDumps = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := core.DefaultConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted negative %s", name)
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
