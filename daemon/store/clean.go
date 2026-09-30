package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/Ebraam-Ashraf/wedjat/daemon/config"
)

const (
	markerName    = ".wedjat-data"
	markerContent = "wedjat-data\nversion=1\n"
)

var dayDatabaseName = regexp.MustCompile("^[0-9]{4}-[0-9]{2}-[0-9]{2}\\.db.*$")

type Lock struct {
	file *os.File
	mu   sync.Mutex
	open bool
}

func AcquireLock(path string) (*Lock, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, errors.New("lock path must be a clean absolute non-root path")
	}
	if err := rejectSymlinkComponents(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("lock directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("daemon lock already held: %s", path)
		}
		return nil, fmt.Errorf("lock daemon: %w", err)
	}
	return &Lock{file: file, open: true}, nil
}

func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.open {
		return nil
	}
	l.open = false
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	return errors.Join(unlockErr, closeErr)
}

func (l *Lock) held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open
}

// PrepareDataDir creates an owned data directory or verifies its marker.
// Callers must acquire and retain the daemon lock before this function.
func PrepareDataDir(lock *Lock, path string, allowHomeDir bool) error {
	if !lock.held() {
		return errors.New("data directory preparation requires the daemon lock")
	}
	if !allowHomeDir {
		if err := config.ValidateDataPath(path); err != nil {
			return err
		}
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("data path is not a directory")
	}
	if err := rejectTreeSymlinks(path); err != nil {
		return err
	}

	markerPath := filepath.Join(path, markerName)
	markerInfo, err := os.Lstat(markerPath)
	if err == nil {
		if !markerInfo.Mode().IsRegular() {
			return errors.New("data marker must be a regular file")
		}
		content, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			return fmt.Errorf("read data marker: %w", readErr)
		}
		if string(content) != markerContent {
			return errors.New("data marker has an unsupported signature or version")
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("refusing non-empty data directory without a Wedjat marker")
	}
	file, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return fmt.Errorf("create data marker: %w", err)
	}
	if _, err = file.WriteString(markerContent); err != nil {
		file.Close()
		return fmt.Errorf("write data marker: %w", err)
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync data marker: %w", err)
	}
	return file.Close()
}

// WipeOwnedData removes only known Wedjat files and dump contents. The marker
// remains so a crash during reset can safely retry the operation next start.
func WipeOwnedData(lock *Lock, path string, allowHomeDir bool) error {
	if !lock.held() {
		return errors.New("data wipe requires the daemon lock")
	}
	if !allowHomeDir {
		if err := config.ValidateDataPath(path); err != nil {
			return err
		}
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	if err := verifyMarker(path); err != nil {
		return err
	}
	if err := rejectTreeSymlinks(path); err != nil {
		return err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		fullPath := filepath.Join(path, name)
		switch {
		case strings.HasPrefix(name, "meta.db"):
			if err := removeKnownDatabaseFile(fullPath); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		case dayDatabaseName.MatchString(name):
			if err := removeKnownDatabaseFile(fullPath); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		case name == "dumps":
			info, err := os.Lstat(fullPath)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return errors.New("dumps must be a directory")
			}
			contents, err := os.ReadDir(fullPath)
			if err != nil {
				return err
			}
			for _, dump := range contents {
				if err := os.RemoveAll(filepath.Join(fullPath, dump.Name())); err != nil {
					return fmt.Errorf("remove dump %s: %w", dump.Name(), err)
				}
			}
		}
	}
	return nil
}

func removeKnownDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-file database path %s", path)
	}
	return os.Remove(path)
}

func verifyMarker(path string) error {
	markerPath := filepath.Join(path, markerName)
	info, err := os.Lstat(markerPath)
	if err != nil {
		return fmt.Errorf("data marker required before wipe: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("data marker must be a regular file")
	}
	content, err := os.ReadFile(markerPath)
	if err != nil {
		return err
	}
	if string(content) != markerContent {
		return errors.New("data marker has an unsupported signature or version")
	}
	return nil
}

func rejectSymlinkComponents(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), current), current) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink path component %s", current)
		}
	}
	return nil
}

func rejectTreeSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink inside data directory: %s", path)
		}
		return nil
	})
}
