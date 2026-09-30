package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
	"time"
)

func TestMaintainExpiresDayFamiliesButKeepsActiveDay(t *testing.T) {
	root := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	s, err := Open(context.Background(), OpenOptions{
		DataDir: filepath.Join(root, "data"), LockPath: filepath.Join(root, "daemon.lock"),
		BootID: func() (string, error) { return "boot", nil }, Clock: clock, ResetOnBoot: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	old := filepath.Join(filepath.Dir(s.MetaPath()), "2026-01-01.db-wal")
	recent := filepath.Join(filepath.Dir(s.MetaPath()), "2026-09-20.db-wal")
	for _, path := range []string{old, recent} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Maintain(context.Background(), MaintenancePolicy{
		MaxSizeBytes: 1 << 20, MinFreeBytes: 0, DayFilesDays: 30,
		ProcessesDays: 90, IncidentsDays: 90, MaxDumps: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired sidecar still exists, stat err=%v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent sidecar removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.MetaPath()), filepath.Base(s.DayPath()))); err != nil {
		t.Fatalf("active day removed: %v", err)
	}
}

func TestMaintainCountsDumpFilesInSizeBudget(t *testing.T) {
	root := t.TempDir()
	s, err := Open(context.Background(), OpenOptions{
		DataDir: filepath.Join(root, "data"), LockPath: filepath.Join(root, "daemon.lock"),
		BootID: func() (string, error) { return "boot", nil }, Clock: func() time.Time { return time.Now().UTC() }, ResetOnBoot: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err := os.Mkdir(filepath.Join(filepath.Dir(s.MetaPath()), "dumps"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.MetaPath()), "dumps", "large.jsonl"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(context.Background(), MaintenancePolicy{
		MaxSizeBytes: 128, MinFreeBytes: 0, DayFilesDays: 30,
		ProcessesDays: 90, IncidentsDays: 90, MaxDumps: 20,
	}); err == nil {
		t.Fatal("maintenance accepted a data directory over its size budget")
	}
}
