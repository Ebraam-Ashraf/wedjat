package store_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
)

func setupOwnedDir(t *testing.T) (*Lock, string) {
	t.Helper()
	root := t.TempDir()
	lock, err := AcquireLock(filepath.Join(root, "run", "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	dataPath := filepath.Join(root, "data")
	if err := PrepareDataDir(lock, dataPath, false); err != nil {
		t.Fatal(err)
	}
	return lock, dataPath
}

func TestPrepareRefusesNonEmptyDirectoryWithoutMarker(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireLock(filepath.Join(root, "run", "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	dataPath := filepath.Join(root, "data")
	if err := os.Mkdir(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "keep.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataDir(lock, dataPath, false); err == nil {
		t.Fatal("non-empty unmarked directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(dataPath, "keep.txt")); err != nil {
		t.Fatalf("refusal removed existing data: %v", err)
	}
}

func TestPrepareAndWipeRefuseSymlinkAtDataPath(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireLock(filepath.Join(root, "run", "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataDir(lock, link, false); err == nil {
		t.Fatal("symlink data path was accepted")
	}
	if err := WipeOwnedData(lock, link, false); err == nil {
		t.Fatal("wipe accepted symlink data path")
	}
}

func TestPrepareAndWipeRefuseSymlinkInsideData(t *testing.T) {
	lock, dataPath := setupOwnedDir(t)
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dataPath, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataDir(lock, dataPath, false); err == nil {
		t.Fatal("directory containing a symlink was accepted")
	}
	if err := WipeOwnedData(lock, dataPath, false); err == nil {
		t.Fatal("wipe proceeded through a symlink")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "preserve" {
		t.Fatalf("symlink target was changed: content=%q err=%v", content, err)
	}
}

func TestPrepareRejectsUnsafePaths(t *testing.T) {
	lock, _ := setupOwnedDir(t)
	for _, path := range []string{"relative", string(filepath.Separator)} {
		if err := PrepareDataDir(lock, path, false); err == nil {
			t.Errorf("accepted unsafe data path %q", path)
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if err := PrepareDataDir(lock, home, false); err == nil {
			t.Errorf("accepted home directory %q", home)
		}
	}
}

func TestAcquireLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "daemon.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := AcquireLock(path); err == nil {
		t.Fatal("second daemon acquired an already-held lock")
	}
}

func TestWipeOnlyRemovesKnownDataAndDumps(t *testing.T) {
	lock, dataPath := setupOwnedDir(t)
	known := []string{
		"meta.db",
		"meta.db-wal",
		"meta.db-shm",
		"2026-09-30.db",
		"2026-09-30.db-wal",
		"2026-09-30.db-journal",
	}
	for _, name := range known {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte("wedjat"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dataPath, "user-notes.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataPath, "dumps"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataPath, "dumps", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "dumps", "nested", "event.jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := WipeOwnedData(lock, dataPath, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range known {
		if _, err := os.Lstat(filepath.Join(dataPath, name)); !os.IsNotExist(err) {
			t.Errorf("known data %q remains (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataPath, ".wedjat-data")); err != nil {
		t.Errorf("ownership marker was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "dumps")); err != nil {
		t.Errorf("dumps directory was removed: %v", err)
	}
	if _, err := os.ReadDir(filepath.Join(dataPath, "dumps")); err != nil {
		t.Errorf("dumps directory is unreadable: %v", err)
	} else if entries, _ := os.ReadDir(filepath.Join(dataPath, "dumps")); len(entries) != 0 {
		t.Errorf("dump contents remain: %v", entries)
	}
	if content, err := os.ReadFile(keep); err != nil || string(content) != "keep" {
		t.Errorf("unrelated file was changed: content=%q err=%v", content, err)
	}
}

func TestWipeRequiresLockAndValidMarker(t *testing.T) {
	_, dataPath := setupOwnedDir(t)
	if err := WipeOwnedData(nil, dataPath, false); err == nil {
		t.Fatal("wipe proceeded without lock")
	}
	lock, err := AcquireLock(filepath.Join(t.TempDir(), "run", "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := os.WriteFile(filepath.Join(dataPath, ".wedjat-data"), []byte("wrong\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WipeOwnedData(lock, dataPath, false); err == nil {
		t.Fatal("wipe accepted invalid marker")
	}
}
