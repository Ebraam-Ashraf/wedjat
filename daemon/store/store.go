package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/config"
	_ "modernc.org/sqlite"
)

type OpenOptions struct {
	DataDir     string
	LockPath    string
	ResetOnBoot bool
	BootID      func() (string, error)
	Clock       func() time.Time
}

type StartupState struct {
	PreviousBootID string
	HeartbeatTS    int64
	BootChanged    bool
	WasClean       bool
	DataReset      bool
}

type Store struct {
	root    string
	lock    *Lock
	meta    *sql.DB
	day     *sql.DB
	dayName string
	clock   func() time.Time
	startup StartupState
	mu      sync.Mutex
	closed  bool
}

func Open(ctx context.Context, options OpenOptions) (_ *Store, err error) {
	if ctx == nil {
		return nil, errors.New("store: nil context")
	}
	if err := config.ValidateDataPath(options.DataDir); err != nil {
		return nil, fmt.Errorf("store data path: %w", err)
	}
	if !filepath.IsAbs(options.LockPath) || filepath.Clean(options.LockPath) != options.LockPath ||
		pathWithin(options.DataDir, options.LockPath) {
		return nil, errors.New("store lock path must be clean, absolute, and outside the data directory")
	}
	if options.BootID == nil {
		options.BootID = ReadBootID
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	bootID, err := options.BootID()
	if err != nil {
		return nil, fmt.Errorf("read boot ID: %w", err)
	}
	bootID = strings.TrimSpace(bootID)
	if bootID == "" {
		return nil, errors.New("store: empty boot ID")
	}

	lock, err := AcquireLock(options.LockPath)
	if err != nil {
		return nil, err
	}
	s := &Store{root: options.DataDir, lock: lock, clock: options.Clock}
	ok := false
	defer func() {
		if !ok {
			_ = s.closeResources()
		}
	}()

	if err := PrepareDataDir(lock, options.DataDir); err != nil {
		return nil, fmt.Errorf("prepare data directory: %w", err)
	}
	metaPath := filepath.Join(options.DataDir, "meta.db")
	metaExisted, err := regularFileExists(metaPath)
	if err != nil {
		return nil, err
	}
	if !metaExisted {
		if err := WipeOwnedData(lock, options.DataDir); err != nil {
			return nil, fmt.Errorf("reset data without metadata DB: %w", err)
		}
	}
	s.meta, err = openSQLite(ctx, metaPath, false, false)
	if err != nil {
		if !options.ResetOnBoot || !metaExisted {
			return nil, fmt.Errorf("open metadata database: %w", err)
		}
		if wipeErr := WipeOwnedData(lock, options.DataDir); wipeErr != nil {
			return nil, fmt.Errorf("recover metadata database open failure (%v): %w", err, wipeErr)
		}
		metaExisted = false
		s.meta, err = openSQLite(ctx, metaPath, false, false)
		if err != nil {
			return nil, fmt.Errorf("recreate metadata database: %w", err)
		}
	}
	if err := checkDatabase(ctx, s.meta); err != nil {
		if !options.ResetOnBoot {
			return nil, fmt.Errorf("metadata database check: %w", err)
		}
		if closeErr := s.meta.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		s.meta = nil
		if wipeErr := WipeOwnedData(lock, options.DataDir); wipeErr != nil {
			return nil, fmt.Errorf("recover corrupt metadata database (%v): %w", err, wipeErr)
		}
		metaExisted = false
		s.meta, err = openSQLite(ctx, metaPath, false, false)
		if err != nil {
			return nil, fmt.Errorf("recreate metadata database: %w", err)
		}
	}

	if err := installSchema(ctx, s.meta, "sql/meta.sql"); err != nil {
		return nil, err
	}
	previousBoot, hasBootState, err := getState(ctx, s.meta, "boot_id")
	if err != nil {
		return nil, err
	}
	previousHeartbeat, hasHeartbeat, err := getState(ctx, s.meta, "heartbeat_ts")
	if err != nil {
		return nil, err
	}
	previousClean, _, err := getState(ctx, s.meta, "clean_shutdown")
	if err != nil {
		return nil, err
	}
	startup := StartupState{
		PreviousBootID: previousBoot,
		BootChanged:    hasBootState && previousBoot != bootID,
		WasClean:       previousClean == "1",
	}
	if hasHeartbeat {
		startup.HeartbeatTS, _ = strconv.ParseInt(previousHeartbeat, 10, 64)
	}
	needsReset := !metaExisted || !hasBootState ||
		(options.ResetOnBoot && previousBoot != bootID)
	startup.DataReset = needsReset
	s.startup = startup
	if needsReset && metaExisted {
		if err := s.meta.Close(); err != nil {
			return nil, err
		}
		s.meta = nil
		if err := WipeOwnedData(lock, options.DataDir); err != nil {
			return nil, err
		}
		if s.meta, err = openSQLite(ctx, metaPath, false, false); err != nil {
			return nil, fmt.Errorf("recreate metadata database: %w", err)
		}
		if err := installSchema(ctx, s.meta, "sql/meta.sql"); err != nil {
			return nil, err
		}
	}

	now := options.Clock().UTC()
	if err := s.openDay(ctx, now); err != nil {
		return nil, err
	}
	if err := s.seedReservedRows(ctx, now.Unix()); err != nil {
		return nil, err
	}
	if hasBootState && previousBoot != bootID && !options.ResetOnBoot {
		if err := s.closeRunningProcessesForReboot(ctx, now); err != nil {
			return nil, err
		}
	}
	if err := s.writeBootState(ctx, bootID, now); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func ReadBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func OpenReadOnly(ctx context.Context, path string, immutable bool) (*sql.DB, error) {
	if ctx == nil {
		return nil, errors.New("store: nil read context")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("read-only database path must be a clean absolute path")
	}
	db, err := openSQLite(ctx, path, true, immutable)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	return db, nil
}

func openSQLite(ctx context.Context, path string, readOnly, immutable bool) (*sql.DB, error) {
	if !readOnly {
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0640)
		if err != nil {
			return nil, err
		}
		var info syscall.Stat_t
		statErr := syscall.Fstat(fd, &info)
		closeErr := syscall.Close(fd)
		if statErr != nil {
			return nil, statErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if info.Mode&syscall.S_IFMT != syscall.S_IFREG {
			return nil, fmt.Errorf("database path is not a regular file: %s", path)
		}
	}

	values := url.Values{}
	if readOnly {
		values.Set("mode", "ro")
		values.Add("_pragma", "query_only(1)")
		values.Add("_pragma", "busy_timeout(5000)")
		if immutable {
			values.Set("immutable", "1")
		}
	} else {
		values.Set("mode", "rw")
		values.Add("_pragma", "journal_mode(WAL)")
		values.Add("_pragma", "synchronous(NORMAL)")
		values.Add("_pragma", "busy_timeout(5000)")
		values.Add("_pragma", "journal_size_limit(67108864)")
		values.Add("_pragma", "foreign_keys(1)")
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: values.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if !readOnly {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Store) openDay(ctx context.Context, now time.Time) error {
	dayName := now.UTC().Format("2006-01-02") + ".db"
	db, err := openSQLite(ctx, filepath.Join(s.root, dayName), false, false)
	if err != nil {
		return fmt.Errorf("open day database %s: %w", dayName, err)
	}
	if err := installSchema(ctx, db, "sql/daily.sql"); err != nil {
		db.Close()
		return err
	}
	s.day, s.dayName = db, dayName
	return nil
}

func (s *Store) rotateDay(ctx context.Context, now time.Time) error {
	dayName := now.UTC().Format("2006-01-02") + ".db"
	if dayName == s.dayName {
		return nil
	}
	if s.day != nil {
		if err := checkpoint(ctx, s.day); err != nil {
			return fmt.Errorf("checkpoint previous day database: %w", err)
		}
		if err := s.day.Close(); err != nil {
			return err
		}
	}
	s.day = nil
	s.dayName = ""
	return s.openDay(ctx, now)
}

func (s *Store) seedReservedRows(ctx context.Context, ts int64) error {
	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO gpus (gpu_id, uuid, first_seen_ts, last_seen_ts) VALUES (0, 'UNKNOWN', ?, ?) ON CONFLICT(gpu_id) DO NOTHING", ts, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO procs (proc_id, boot_id, tgid, start_ticks, command, first_seen_ts) VALUES (0, 'unknown', 0, 0, '<unattributed>', ?) ON CONFLICT(proc_id) DO NOTHING", ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) closeRunningProcessesForReboot(ctx context.Context, now time.Time) error {
	previousHeartbeat, exists, err := getState(ctx, s.meta, "heartbeat_ts")
	if err != nil {
		return err
	}
	endTS := now.Unix()
	if exists {
		if parsed, parseErr := strconv.ParseInt(previousHeartbeat, 10, 64); parseErr == nil {
			endTS = parsed
		}
	}
	_, err = s.meta.ExecContext(ctx, "UPDATE procs SET end_ts = ?, end_reason = 'reboot' WHERE proc_id <> 0 AND end_ts IS NULL", endTS)
	return err
}

func (s *Store) writeBootState(ctx context.Context, bootID string, now time.Time) error {
	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := putState(ctx, tx, "heartbeat_ts", strconv.FormatInt(now.Unix(), 10)); err != nil {
		return err
	}
	if err := putState(ctx, tx, "clean_shutdown", "0"); err != nil {
		return err
	}
	if err := putState(ctx, tx, "schema_version", strconv.Itoa(schemaVersion)); err != nil {
		return err
	}
	if err := putState(ctx, tx, "boot_id", bootID); err != nil {
		return err
	}
	return tx.Commit()
}

func putState(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO daemon_state(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v", key, value)
	return err
}

func HasDataForBoot(ctx context.Context, db *sql.DB, bootID string) (bool, error) {
	if ctx == nil || db == nil || strings.TrimSpace(bootID) == "" {
		return false, errors.New("store: database and boot ID are required")
	}
	stored, exists, err := getState(ctx, db, "boot_id")
	return exists && stored == bootID, err
}

func getState(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var value string
	err := db.QueryRowContext(ctx, "SELECT v FROM daemon_state WHERE k = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("expected regular file at %s", path)
	}
	return true, nil
}

func checkDatabase(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("quick_check: %s", result)
		}
	}
	return rows.Err()
}

func checkpoint(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("WAL checkpoint busy (%d frames, %d checkpointed)", logFrames, checkpointed)
	}
	return nil
}

func (s *Store) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("store: nil shutdown context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if s.meta != nil {
		tx, err := s.meta.BeginTx(ctx, nil)
		if err == nil {
			err = putState(ctx, tx, "heartbeat_ts", strconv.FormatInt(s.clock().UTC().Unix(), 10))
			if err == nil {
				err = putState(ctx, tx, "clean_shutdown", "0")
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("mark shutdown in progress: %w", err))
		}
	}
	if err := checkpoint(ctx, s.day); err != nil {
		errs = append(errs, fmt.Errorf("checkpoint day database: %w", err))
	}
	if err := checkpoint(ctx, s.meta); err != nil {
		errs = append(errs, fmt.Errorf("checkpoint metadata database: %w", err))
	}
	if len(errs) == 0 && s.meta != nil {
		tx, err := s.meta.BeginTx(ctx, nil)
		if err == nil {
			err = putState(ctx, tx, "heartbeat_ts", strconv.FormatInt(s.clock().UTC().Unix(), 10))
			if err == nil {
				err = putState(ctx, tx, "clean_shutdown", "1")
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err == nil {
			err = checkpoint(ctx, s.meta)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("finish clean shutdown: %w", err))
			resetCtx, resetCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer resetCancel()
			resetTx, resetErr := s.meta.BeginTx(resetCtx, nil)
			if resetErr == nil {
				resetErr = putState(resetCtx, resetTx, "clean_shutdown", "0")
				if resetErr == nil {
					resetErr = resetTx.Commit()
				} else {
					_ = resetTx.Rollback()
				}
			}
			if resetErr != nil {
				errs = append(errs, fmt.Errorf("mark shutdown unclean: %w", resetErr))
			}
		}
	}
	errs = append(errs, s.closeResources()...)
	return errors.Join(errs...)
}

func (s *Store) closeResources() []error {
	var errs []error
	if s.day != nil {
		if err := s.day.Close(); err != nil {
			errs = append(errs, err)
		}
		s.day = nil
	}
	if s.meta != nil {
		if err := s.meta.Close(); err != nil {
			errs = append(errs, err)
		}
		s.meta = nil
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil {
			errs = append(errs, err)
		}
		s.lock = nil
	}
	return errs
}

func (s *Store) Startup() StartupState {
	return s.startup
}

func (s *Store) MetaPath() string {
	return filepath.Join(s.root, "meta.db")
}

func (s *Store) DayPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dayName == "" {
		return ""
	}
	return filepath.Join(s.root, s.dayName)
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
