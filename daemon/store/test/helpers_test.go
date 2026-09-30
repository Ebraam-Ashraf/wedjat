package store_test

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
	_ "modernc.org/sqlite"
)

func fixedClock(value time.Time) func() time.Time {
	return func() time.Time { return value }
}

// mutableClock returns a clock and a setter, so a test can advance time the
// way the daemon's tickers do. fixedClock captures its argument by value and
// cannot be moved after the fact.
func mutableClock(start time.Time) (func() time.Time, func(time.Time)) {
	var mu sync.Mutex
	current := start
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	advance := func(next time.Time) {
		mu.Lock()
		defer mu.Unlock()
		current = next
	}
	return clock, advance
}

func testOpenOptions(root, boot string, now time.Time, reset bool) OpenOptions {
	return OpenOptions{
		DataDir:     filepath.Join(root, "data"),
		LockPath:    filepath.Join(root, "run", "daemon.lock"),
		ResetOnBoot: reset,
		BootID:      func() (string, error) { return boot, nil },
		Clock:       fixedClock(now),
	}
}

func int64ptr(value int64) *int64 { return &value }

func openTestDB(t *testing.T, path string, readOnly bool) *sql.DB {
	t.Helper()
	values := url.Values{}
	if readOnly {
		values.Set("mode", "ro")
		values.Add("_pragma", "query_only(1)")
	} else {
		values.Set("mode", "rw")
		values.Add("_pragma", "foreign_keys(1)")
	}
	values.Add("_pragma", "busy_timeout(5000)")
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: values.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
