package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type MaintenancePolicy struct {
	MaxSizeBytes  int64
	MinFreeBytes  int64
	DayFilesDays  int
	ProcessesDays int
	IncidentsDays int
	MaxDumps      int
}

type ownedFile struct {
	path string
	name string
	day  string
	size int64
	date time.Time
}

// Maintain enforces database and file retention while holding the store lock.
// Call it during bootstrap and periodically; it never removes the active day DB.
func (s *Store) Maintain(ctx context.Context, policy MaintenancePolicy) error {
	if ctx == nil {
		return errors.New("store: nil maintenance context")
	}
	if policy.MaxSizeBytes <= 0 || policy.MinFreeBytes < 0 || policy.DayFilesDays <= 0 ||
		policy.ProcessesDays <= 0 || policy.IncidentsDays <= 0 || policy.MaxDumps <= 0 {
		return errors.New("store: invalid maintenance policy")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.meta == nil || s.lock == nil || !s.lock.held() {
		return errors.New("store: maintenance requires an open store and daemon lock")
	}

	now := s.clock().UTC()
	if err := s.pruneRows(ctx, now, policy); err != nil {
		return err
	}
	if err := s.pruneDumps(ctx, now, policy); err != nil {
		return err
	}
	files, total, err := s.dayFiles()
	if err != nil {
		return err
	}
	cutoff := utcDay(now).AddDate(0, 0, -policy.DayFilesDays)
	activeDay := strings.TrimSuffix(s.dayName, ".db")
	for _, file := range files {
		if file.day == activeDay {
			continue
		}
		if file.date.Before(cutoff) {
			if err := os.Remove(file.path); err != nil {
				return fmt.Errorf("remove expired day file %s: %w", file.name, err)
			}
			total -= file.size
		}
	}
	files, total, err = s.dayFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		if total <= policy.MaxSizeBytes {
			break
		}
		if file.day == activeDay {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			return fmt.Errorf("remove day file for size budget %s: %w", file.name, err)
		}
		total -= file.size
	}
	if total > policy.MaxSizeBytes {
		return fmt.Errorf("storage exceeds max size: %d > %d bytes", total, policy.MaxSizeBytes)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return fmt.Errorf("check free space: %w", err)
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free < policy.MinFreeBytes {
		return fmt.Errorf("storage free space below minimum: %d < %d bytes", free, policy.MinFreeBytes)
	}
	return nil
}

func (s *Store) pruneRows(ctx context.Context, now time.Time, p MaintenancePolicy) error {
	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	procCutoff := now.AddDate(0, 0, -p.ProcessesDays).Unix()
	incidentCutoff := now.AddDate(0, 0, -p.IncidentsDays).Unix()
	if _, err := tx.ExecContext(ctx, "DELETE FROM incidents WHERE last_ts < ?", incidentCutoff); err != nil {
		return fmt.Errorf("prune incidents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM procs WHERE proc_id <> 0 AND end_ts IS NOT NULL AND end_ts < ?", procCutoff); err != nil {
		return fmt.Errorf("prune finished processes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM daemon_log WHERE ts < ?", incidentCutoff); err != nil {
		return fmt.Errorf("prune daemon log: %w", err)
	}
	return tx.Commit()
}

func (s *Store) pruneDumps(ctx context.Context, now time.Time, p MaintenancePolicy) error {
	cutoff := now.AddDate(0, 0, -p.IncidentsDays).Unix()
	rows, err := s.meta.QueryContext(ctx, "SELECT dump_id, path, created_ts FROM dumps ORDER BY created_ts DESC, dump_id DESC")
	if err != nil {
		return err
	}
	type dump struct {
		id      int64
		path    string
		created int64
	}
	var all []dump
	for rows.Next() {
		var d dump
		if err := rows.Scan(&d.id, &d.path, &d.created); err != nil {
			rows.Close()
			return err
		}
		all = append(all, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i, d := range all {
		if i < p.MaxDumps && d.created >= cutoff {
			continue
		}
		if err := removeOwnedDump(s.root, d.path); err != nil {
			return err
		}
		if _, err := s.meta.ExecContext(ctx, "DELETE FROM dumps WHERE dump_id = ?", d.id); err != nil {
			return fmt.Errorf("prune dump row %d: %w", d.id, err)
		}
	}
	return nil
}

func removeOwnedDump(root, name string) error {
	if filepath.IsAbs(name) {
		return fmt.Errorf("refusing dump path outside data directory: %q", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		!(clean == "dumps" || strings.HasPrefix(clean, "dumps"+string(filepath.Separator))) {
		return fmt.Errorf("refusing dump path outside dumps directory: %q", name)
	}
	path := filepath.Join(root, clean)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular dump path: %s", path)
	}
	return os.Remove(path)
}

func (s *Store) dayFiles() ([]ownedFile, int64, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, 0, err
	}
	var files []ownedFile
	var total int64
	for _, entry := range entries {
		if !dayDatabaseName.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			return nil, 0, fmt.Errorf("refusing non-regular day database path %s", path)
		}
		date, err := time.Parse("2006-01-02", entry.Name()[:10])
		if err != nil {
			continue
		}
		files = append(files, ownedFile{path: path, name: entry.Name(), day: entry.Name()[:10], size: info.Size(), date: date})
		total += info.Size()
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "meta.db") {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			return nil, 0, fmt.Errorf("refusing non-regular metadata database path %s", path)
		}
		total += info.Size()
	}
	dumpsDir := filepath.Join(s.root, "dumps")
	if info, err := os.Lstat(dumpsDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, 0, errors.New("dumps path must be a real directory")
		}
		err = filepath.WalkDir(dumpsDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != dumpsDir && entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink in dumps directory: %s", path)
			}
			if path != dumpsDir && entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				total += info.Size()
			}
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].date.Before(files[j].date) })
	return files, total, nil
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
