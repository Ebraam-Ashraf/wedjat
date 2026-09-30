package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
	"time"
)

func TestOpenAndSameBootKeepsDataNewBootResets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	metaDB := openTestDB(t, s.MetaPath(), false)
	if _, err := metaDB.ExecContext(ctx, "INSERT INTO incidents(type, first_ts, last_ts, dedupe_key) VALUES ('xid', 1, 1, 'xid:0:0')"); err != nil {
		t.Fatal(err)
	}
	metaDB.Close()
	oldDayPath := s.DayPath()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, testOpenOptions(root, "boot-a", now.Add(time.Hour), true))
	if err != nil {
		t.Fatal(err)
	}
	metaDB = openTestDB(t, s.MetaPath(), false)
	var incidents int
	if err := metaDB.QueryRowContext(ctx, "SELECT count(*) FROM incidents").Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 {
		t.Fatalf("same-boot restart lost incident: count=%d", incidents)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	nextBoot := now.Add(24 * time.Hour)
	s, err = Open(ctx, testOpenOptions(root, "boot-b", nextBoot, true))
	if err != nil {
		t.Fatal(err)
	}
	metaDB = openTestDB(t, s.MetaPath(), false)
	if err := metaDB.QueryRowContext(ctx, "SELECT count(*) FROM incidents").Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if incidents != 0 {
		t.Fatalf("new boot retained old incidents: count=%d", incidents)
	}
	if err := metaDB.QueryRowContext(ctx, "SELECT v FROM daemon_state WHERE k='boot_id'").Scan(new(string)); err != nil {
		t.Fatalf("new boot ID was not recorded: %v", err)
	}
	metaDB.Close()
	if _, err := os.Stat(oldDayPath); !os.IsNotExist(err) {
		t.Fatalf("old day DB survived reset: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestResetDisabledClosesRunningProcessesAtPreviousHeartbeat(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	firstTime := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", firstTime, false))
	if err != nil {
		t.Fatal(err)
	}
	metaDB := openTestDB(t, s.MetaPath(), false)
	if _, err := metaDB.ExecContext(ctx, "INSERT INTO procs(boot_id,tgid,start_ticks,command,first_seen_ts) VALUES ('boot-a', 412, 90, 'job', 100)"); err != nil {
		t.Fatal(err)
	}
	metaDB.Close()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	secondTime := firstTime.Add(48 * time.Hour)
	s, err = Open(ctx, testOpenOptions(root, "boot-b", secondTime, false))
	if err != nil {
		t.Fatal(err)
	}
	metaDB = openTestDB(t, s.MetaPath(), false)
	var endTS int64
	var reason string
	if err := metaDB.QueryRowContext(ctx, "SELECT end_ts, end_reason FROM procs WHERE tgid=412").Scan(&endTS, &reason); err != nil {
		t.Fatal(err)
	}
	if reason != "reboot" || endTS != firstTime.Unix() {
		t.Fatalf("reconciled process = (%d, %q), want (%d, reboot)", endTS, reason, firstTime.Unix())
	}
	metaDB.Close()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStoreUsesUTCDateAndReadOnlyConnection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localTime := time.Date(2026, 10, 1, 0, 30, 0, 0, time.FixedZone("local", 2*60*60))
	s, err := Open(ctx, testOpenOptions(root, "boot-a", localTime, true))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(s.DayPath()), "2026-09-30.db"; got != want {
		t.Fatalf("UTC day file = %s, want %s", got, want)
	}
	readDB, err := OpenReadOnly(ctx, s.MetaPath(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readDB.ExecContext(ctx, "UPDATE daemon_state SET v='bad' WHERE k='boot_id'"); err == nil {
		t.Fatal("read-only connection allowed a write")
	}
	if err := readDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOpenHoldsExclusiveLock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	options := testOpenOptions(root, "boot-a", time.Now().UTC(), true)
	s, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, options); err == nil {
		t.Fatal("second store opened while daemon lock was held")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
