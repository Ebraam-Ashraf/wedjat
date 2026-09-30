package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
)

// The heartbeat is what a reader uses to decide whether the daemon is alive.
// If it only advances at startup, a healthy daemon is indistinguishable from a
// dead one, so these tests pin the advancing behaviour.
func TestHeartbeatAdvancesOnDemand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	clock, advance := mutableClock(start)
	opts := testOpenOptions(root, "boot-a", start, true)
	opts.Clock = clock
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	metaDB := openTestDB(t, s.MetaPath(), true)
	read := func() int64 {
		t.Helper()
		var value string
		if err := metaDB.QueryRowContext(ctx, "SELECT v FROM daemon_state WHERE k='heartbeat_ts'").Scan(&value); err != nil {
			t.Fatal(err)
		}
		var parsed int64
		if _, err := fmt.Sscan(value, &parsed); err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	initial := read()
	if initial != start.Unix() {
		t.Fatalf("initial heartbeat = %d, want %d", initial, start.Unix())
	}

	// Advance the clock and beat: the stored value must follow.
	advance(start.Add(90 * time.Second))
	if err := s.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != start.Add(90*time.Second).Unix() {
		t.Fatalf("heartbeat after beat = %d, want %d", got, start.Add(90*time.Second).Unix())
	}

	// A second beat moves it again; a frozen timestamp is the bug this guards.
	advance(start.Add(180 * time.Second))
	if err := s.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != start.Add(180*time.Second).Unix() {
		t.Fatalf("heartbeat after second beat = %d, want %d", got, start.Add(180*time.Second).Unix())
	}
}

func TestHeartbeatAgeTracksTheClock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	clock, advance := mutableClock(start)
	opts := testOpenOptions(root, "boot-a", start, true)
	opts.Clock = clock
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	age, err := s.HeartbeatAge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age != 0 {
		t.Fatalf("fresh heartbeat age = %v, want 0", age)
	}

	// Without a beat the age grows, which is how a reader detects a stall.
	advance(start.Add(5 * time.Minute))
	age, err = s.HeartbeatAge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age != 5*time.Minute {
		t.Fatalf("stale heartbeat age = %v, want 5m", age)
	}

	// Beating resets it.
	if err := s.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	age, err = s.HeartbeatAge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age != 0 {
		t.Fatalf("age after beat = %v, want 0", age)
	}
}

func TestHeartbeatRejectsNilContext(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	//lint:ignore SA1012 deliberately passing nil to verify the guard
	if err := s.Heartbeat(nil); err == nil {
		t.Fatal("expected a nil context to be rejected")
	}
}

func TestHeartbeatAfterCloseIsRejected(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx); err == nil {
		t.Fatal("expected a beat after close to be rejected")
	}
}
