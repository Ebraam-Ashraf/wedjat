package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
	"time"
)

func TestMissingMetaRetriesInterruptedReset(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	oldDay := s.DayPath()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "data", "meta.db")); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, testOpenOptions(root, "boot-b", now.Add(24*time.Hour), true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDay); !os.IsNotExist(err) {
		t.Fatalf("partial prior-boot day data was not reset: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHasDataForBootRejectsStaleHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testOpenOptions(t.TempDir(), "boot-current", time.Now().UTC(), true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	metaDB := openTestDB(t, s.MetaPath(), true)
	if current, err := HasDataForBoot(ctx, metaDB, "boot-current"); err != nil || !current {
		t.Fatalf("current boot freshness = %t, err=%v", current, err)
	}
	if current, err := HasDataForBoot(ctx, metaDB, "boot-previous"); err != nil || current {
		t.Fatalf("stale boot freshness = %t, err=%v", current, err)
	}
}
