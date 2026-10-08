package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed sql/*.sql
var schemaFiles embed.FS

const schemaVersion = 1
const dayFileSuffix = ".db"

// DB manages SQLite databases for daemon storage.
type DB struct {
	root    string
	meta    *sql.DB
	day     *sql.DB
	dayName string
	mu      sync.Mutex
	closed  bool
}

// dayLayout names one daily database file per UTC day.
const dayLayout = "2006-01-02"

// checkOpen reports whether the database is still usable. Callers must hold mu.
func (db *DB) checkOpen() error {
	if db.closed {
		return errors.New("db: closed")
	}
	return nil
}

// OpenDB opens or creates the metadata and daily databases.
func OpenDB(ctx context.Context, dataDir string) (*DB, error) {
	if ctx == nil {
		return nil, errors.New("db: nil context")
	}

	// Create data directory
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	// Create data directory marker so uninstall purge knows it's safe to delete
	markerPath := filepath.Join(dataDir, ".wedjat-data")
	if err := os.WriteFile(markerPath, []byte("wedjat-data\nversion=1\n"), 0644); err != nil {
		return nil, fmt.Errorf("create data marker: %w", err)
	}

	db := &DB{root: dataDir}

	// Open metadata database
	metaPath := filepath.Join(dataDir, "meta.db")
	var err error
	db.meta, err = openSQLite(ctx, metaPath)
	if err != nil {
		return nil, fmt.Errorf("open metadata database: %w", err)
	}

	// Create metadata schema
	if err := installSchema(ctx, db.meta, "sql/meta.sql"); err != nil {
		db.meta.Close()
		return nil, fmt.Errorf("create metadata schema: %w", err)
	}

	// Open today's daily database
	today := time.Now().UTC().Format(dayLayout)
	if err := db.rotateDayDB(ctx, today); err != nil {
		db.meta.Close()
		return nil, fmt.Errorf("open daily database: %w", err)
	}

	return db, nil
}

// rotateDayDB opens or creates the daily database for the given date.
// Caller must hold db.mu.
func (db *DB) rotateDayDB(ctx context.Context, date string) error {
	if db.dayName == date && db.day != nil {
		return nil
	}

	// Close previous day DB if open
	if db.day != nil {
		db.day.Close()
	}

	dayPath := filepath.Join(db.root, date+".db")
	var err error
	db.day, err = openSQLite(ctx, dayPath)
	if err != nil {
		return fmt.Errorf("open daily database: %w", err)
	}

	// Create daily schema
	if err := installSchema(ctx, db.day, "sql/daily.sql"); err != nil {
		db.day.Close()
		return fmt.Errorf("create daily schema: %w", err)
	}

	db.dayName = date
	return nil
}

// openSQLite opens a SQLite database with the settings the daemon relies on.
//
// The connection pool is pinned to a single connection on purpose. SQLite
// applies pragmas per connection, so foreign keys could not be relied on with
// a larger pool, and a single writer also removes any chance of two
// transactions colliding inside the same process.
func openSQLite(ctx context.Context, path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_sync=NORMAL&_busy_timeout=5000", path)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	// Verify connection
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, err
	}

	// Without this every REFERENCES clause and ON DELETE CASCADE in the
	// schema is silently inert, because SQLite defaults it off.
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		database.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	return database, nil
}

// WriteHeartbeat updates the daemon heartbeat timestamp.
func (db *DB) WriteHeartbeat(ctx context.Context) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return err
	}

	now := time.Now().Unix()
	_, err := db.meta.ExecContext(ctx,
		`INSERT INTO daemon_state (k, v) VALUES ('heartbeat_ts', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		fmt.Sprint(now))
	return err
}

// WriteBootID records the current boot ID.
func (db *DB) WriteBootID(ctx context.Context, bootID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return err
	}

	// /proc/sys/kernel/random/boot_id ends in a newline; storing it verbatim
	// would make later comparisons against a trimmed boot ID fail.
	_, err := db.meta.ExecContext(ctx,
		`INSERT INTO daemon_state (k, v) VALUES ('boot_id', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		strings.TrimSpace(bootID))
	return err
}

// WriteCleanShutdown marks shutdown state.
func (db *DB) WriteCleanShutdown(ctx context.Context, clean bool) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return err
	}

	value := "0"
	if clean {
		value = "1"
	}

	_, err := db.meta.ExecContext(ctx,
		`INSERT INTO daemon_state (k, v) VALUES ('clean_shutdown', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		value)
	return err
}

// Close checkpoints both databases and closes them.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}
	db.closed = true

	// Checkpoint with a hard timeout so shutdown cannot hang indefinitely.
	// Use a fresh context since the caller's context is likely cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var errs []error
	if db.day != nil {
		if err := checkpoint(ctx, db.day); err != nil {
			errs = append(errs, fmt.Errorf("checkpoint daily db: %w", err))
		}
		if err := db.day.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close daily db: %w", err))
		}
		db.day = nil
	}
	if db.meta != nil {
		if err := checkpoint(ctx, db.meta); err != nil {
			errs = append(errs, fmt.Errorf("checkpoint meta db: %w", err))
		}
		if err := db.meta.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close meta db: %w", err))
		}
		db.meta = nil
	}

	return errors.Join(errs...)
}

// checkpoint folds the write-ahead log back into the database file so the
// day file is self-contained and readable without its -wal companion.
func checkpoint(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return nil
	}
	var busy, frames, checkpointed int
	if err := database.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &frames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("wal checkpoint busy (%d frames, %d checkpointed)", frames, checkpointed)
	}
	return nil
}

// installSchema brings a database up to schemaVersion.
//
// The schema file is executed as a single blob so that multi-statement
// constructs (triggers, views with semicolons inside BEGIN…END) are never
// split mid-statement.
//
// PRAGMA user_version is intentionally set *after* the transaction commits.
// SQLite executes PRAGMA user_version immediately, outside of any transaction
// semantics, so placing it inside the transaction gives a false sense of
// atomicity: the version would be bumped even if Commit() later failed,
// leaving the database at the new version number but with no tables.
func installSchema(ctx context.Context, database *sql.DB, name string) error {
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than the supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}

	contents, err := schemaFiles.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read schema %s: %w", name, err)
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Set the version only after the schema transaction has been durably
	// committed.  A failure here leaves the version at 0, so the next
	// startup will re-apply the schema idempotently (CREATE TABLE IF NOT
	// EXISTS) and then succeed in bumping the version.
	if _, err := database.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// RotateDay closes the currently open daily database and opens the one for at.
func (db *DB) RotateDay(ctx context.Context, at time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return err
	}
	return db.rotateDayDB(ctx, at.UTC().Format(dayLayout))
}

// PruneDayFiles deletes daily databases older than keepDays and returns how
// many were removed. A keepDays of zero or less keeps everything.
func (db *DB) PruneDayFiles(ctx context.Context, keepDays int, now time.Time) (int, error) {
	if keepDays <= 0 {
		return 0, nil
	}

	cutoff := now.UTC().AddDate(0, 0, -keepDays)

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(db.root)
	if err != nil {
		return 0, fmt.Errorf("read data directory: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != dayFileSuffix {
			continue
		}

		date, err := time.Parse(dayLayout, entry.Name()[:len(entry.Name())-len(dayFileSuffix)])
		if err != nil {
			continue
		}
		if !date.Before(cutoff) {
			continue
		}

		path := filepath.Join(db.root, entry.Name())
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			walPath := path + suffix
			if err := os.Remove(walPath); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", walPath, err)
			}
		}
		removed++
	}
	return removed, nil
}

// QueryMeta executes a query on the metadata database.
func (db *DB) QueryMeta(ctx context.Context, query string, dest any) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return err
	}
	return db.meta.QueryRowContext(ctx, query).Scan(dest)
}

// GetGPUIdentity fetches GPU identity by ID.
func (db *DB) GetGPUIdentity(ctx context.Context, gpuID int64) (*GPUIdentity, error) {
	return db.getGPUIdentity(ctx, gpuID)
}

// GetGPUDBIDByUUID returns the internal database PK for a GPU identified by UUID.
// Returns 0 if not found.
func (db *DB) GetGPUDBIDByUUID(ctx context.Context, uuid string) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}
	var id int64
	err := db.meta.QueryRowContext(ctx, `SELECT gpu_id FROM gpus WHERE uuid = ?`, uuid).Scan(&id)
	if err != nil {
		return 0, nil // not found is not fatal
	}
	return id, nil
}

// GetGPUDBIDForHistory looks up the internal DB PK for a GPU by UUID for use
// in history/aggregates queries.  Unlike GetGPUDBIDByUUID it returns an error
// when the GPU is not registered so the HTTP handler can return 404.
func (db *DB) GetGPUDBIDForHistory(ctx context.Context, uuid string) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}
	var id int64
	err := db.meta.QueryRowContext(ctx, `SELECT gpu_id FROM gpus WHERE uuid = ?`, uuid).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("gpu %q not found", uuid)
	}
	return id, nil
}

// GetProcessIdentity fetches process identity by ID.
func (db *DB) GetProcessIdentity(ctx context.Context, procID int64) (*ProcessIdentity, error) {
	return db.getProcessIdentity(ctx, procID)
}

// PruneMeta removes ended process and incident rows outside their retention
// windows. Non-positive windows disable pruning for that table.
func (db *DB) PruneMeta(ctx context.Context, now time.Time, processesDays, incidentsDays int) (int64, int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, 0, err
	}
	tx, err := db.meta.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var procs, incidents int64
	if processesDays > 0 {
		cutoff := now.UTC().AddDate(0, 0, -processesDays).Unix()
		res, err := tx.ExecContext(ctx, `DELETE FROM procs WHERE end_ts IS NOT NULL AND end_ts < ?`, cutoff)
		if err != nil {
			return 0, 0, fmt.Errorf("prune processes: %w", err)
		}
		procs, err = res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
	}
	if incidentsDays > 0 {
		cutoff := now.UTC().AddDate(0, 0, -incidentsDays).Unix()
		res, err := tx.ExecContext(ctx, `DELETE FROM incidents WHERE last_ts < ?`, cutoff)
		if err != nil {
			return 0, 0, fmt.Errorf("prune incidents: %w", err)
		}
		incidents, err = res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return procs, incidents, nil
}

// RunDayTimer rotates and prunes daily databases at midnight UTC.
// Run it in its own goroutine; it returns when ctx is cancelled.
func RunDayTimer(ctx context.Context, database *DB, keepDays int) {
	RunDayTimerWithMeta(ctx, database, keepDays, 0, 0)
}

func RunDayTimerWithMeta(ctx context.Context, database *DB, keepDays, processesDays, incidentsDays int) {
	for {
		if err := runDayCycleWithMeta(ctx, database, keepDays, processesDays, incidentsDays); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("day timer: %v", err)
		}

		timer := time.NewTimer(time.Until(nextUTCMidnight()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runDayCycle(ctx context.Context, database *DB, keepDays int) error {
	return runDayCycleWithMeta(ctx, database, keepDays, 0, 0)
}

func runDayCycleWithMeta(ctx context.Context, database *DB, keepDays, processesDays, incidentsDays int) error {
	now := time.Now().UTC()

	if err := database.RotateDay(ctx, now); err != nil {
		return err
	}

	removed, err := database.PruneDayFiles(ctx, keepDays, now)
	if err != nil {
		return err
	}
	if removed > 0 {
		log.Printf("day timer: removed %d day file(s) older than %d day(s)", removed, keepDays)
	}
	procs, incidents, err := database.PruneMeta(ctx, now, processesDays, incidentsDays)
	if err != nil {
		return err
	}
	if procs > 0 || incidents > 0 {
		log.Printf("day timer: pruned %d process row(s) and %d incident row(s)", procs, incidents)
	}
	return nil
}

func nextUTCMidnight() time.Time {
	now := time.Now().UTC()
	return now.Truncate(24 * time.Hour).Add(24 * time.Hour)
}
