package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runUninstall performs the Wedjat uninstall procedure.
// It removes binaries, the systemd unit, and BPF objects.
// User data (/var/lib/wedjat) and config (/etc/wedjat) are preserved
// unless the user explicitly confirms deletion.
func runUninstall() {
	if os.Geteuid() != 0 {
		log.Fatal("wedjat uninstall must be run as root (sudo wedjat --uninstall)")
	}

	fmt.Println("[1/4] Stopping daemon...")
	_ = exec.Command("systemctl", "disable", "--now", "wedjatd").Run()

	fmt.Println("[2/4] Removing unit + binaries...")
	remove("/etc/systemd/system/wedjatd.service")
	remove("/usr/local/bin/wedjatd")
	remove("/usr/local/bin/wedjat")
	removeAll("/usr/local/lib/wedjat")
	_ = exec.Command("systemctl", "daemon-reload").Run()

	fmt.Println("[3/4] Configuration and history are preserved by default.")
	fmt.Print("Delete Wedjat configuration and collected data? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(strings.ToLower(ans))

	if ans == "y" || ans == "yes" {
		purgeData()
	} else {
		fmt.Println("Configuration and history preserved.")
	}

	fmt.Println("[4/4] Done.")
}

// purgeData removes Wedjat databases, config, and dumps.
// It requires the data-marker file to be valid before deleting anything,
// preventing accidental deletion of unrelated directories.
func purgeData() {
	const dataDir = "/var/lib/wedjat"
	const configDir = "/etc/wedjat"
	const marker = "/var/lib/wedjat/.wedjat-data"

	// Safety: refuse if the data marker is missing, invalid, or a symlink.
	info, err := os.Lstat(marker)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintln(os.Stderr, "Refusing purge: the Wedjat data marker is missing, invalid, or a symlink.")
		os.Exit(1)
	}
	expected := "wedjat-data\nversion=1\n"
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != expected {
		fmt.Fprintln(os.Stderr, "Refusing purge: the Wedjat data marker content is invalid.")
		os.Exit(1)
	}

	// Check for symlinks inside the data directory.
	hasSymlink := false
	_ = filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			hasSymlink = true
			return filepath.SkipAll
		}
		return nil
	})
	if hasSymlink {
		fmt.Fprintln(os.Stderr, "Refusing purge: a symlink exists in the data directory.")
		os.Exit(1)
	}

	// Remove database files (meta.db*, YYYY-MM-DD.db*, r-YYYY-MM-DD.db*).
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "meta.db") || isDateDB(name) {
			remove(filepath.Join(dataDir, name))
		}
	}

	// Remove dumps directory contents.
	dumpsDir := filepath.Join(dataDir, "dumps")
	if info, err := os.Stat(dumpsDir); err == nil && info.IsDir() {
		removeAll(dumpsDir)
		_ = os.MkdirAll(dumpsDir, 0750)
	}

	// Remove config file.
	configFile := filepath.Join(configDir, "config.yaml")
	if info, err := os.Lstat(configFile); err == nil && info.Mode()&os.ModeSymlink == 0 {
		remove(configFile)
	}
	_ = os.Remove(configDir) // rmdir only if empty

	fmt.Println("Known Wedjat data and config files removed; the ownership marker remains.")
}

// isDateDB checks if a filename matches the pattern for daily database files.
func isDateDB(name string) bool {
	// Matches: YYYY-MM-DD.db* or r-YYYY-MM-DD.db*
	s := name
	if strings.HasPrefix(s, "r-") {
		s = s[2:]
	}
	if len(s) < 13 { // YYYY-MM-DD.db
		return false
	}
	if s[4] != '-' || s[7] != '-' {
		return false
	}
	return strings.Contains(s[10:], ".db")
}

func remove(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: could not remove %s: %v", path, err)
	}
}

func removeAll(path string) {
	if err := os.RemoveAll(path); err != nil {
		log.Printf("Warning: could not remove %s: %v", path, err)
	}
}
