package main

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"

	"github.com/Ebraam-Ashraf/wedjat/daemon/internal/daemon"
)

// rawPaths uses relative paths that are resolved to absolute below so the lock
// validator in acquireLock (which requires a clean absolute path) is satisfied
// regardless of where the binary is invoked from.
var rawPaths = daemon.Paths{
	ConfigFile:  "./dev/etc/wedjat/config.yaml",
	DataDir:     "./dev/var/lib/wedjat",
	LockFile:    "./dev/run/wedjat.lock",
	SocketPath:  "./dev/run/wedjat.sock",
	SocketGroup: "", // No group in dev mode; socket is owned by the running user.
}

func main() {
	log.Println("==> Starting wedjatd-dev in DEVELOPMENT MODE")

	paths, err := resolvePaths(rawPaths)
	if err != nil {
		log.Fatalf("FATAL: resolve paths: %v", err)
	}

	log.Printf("    Config:  %s", paths.ConfigFile)
	log.Printf("    DataDir: %s", paths.DataDir)
	log.Printf("    Socket:  %s", paths.SocketPath)

	if err := daemon.Run(paths, "wedjatd-dev is running. Press Ctrl+C to stop.", devPreRun); err != nil {
		log.Fatalf("FATAL: %v", err)
	}
}

// devPreRun aborts startup when the system daemon is already running.
// Allowing both to run simultaneously would cause eBPF attach conflicts and
// duplicate database writes.
func devPreRun() error {
	// exec.Command.Run returns a non-zero exit status when systemctl reports
	// the unit is inactive, failed, or unknown — and also when systemctl itself
	// is not available (e.g. inside a container). Both cases are safe to ignore:
	// err != nil means either the service is not active or we cannot check,
	// and in either case there is no conflict to guard against.
	cmd := exec.Command("systemctl", "is-active", "--quiet", "wedjatd.service")
	if cmd.Run() == nil {
		return fmt.Errorf("wedjatd.service is running. Stop it first to prevent eBPF conflicts:\n  sudo systemctl stop wedjatd")
	}
	return nil
}

// resolvePaths converts every relative path in p to a clean absolute path.
func resolvePaths(p daemon.Paths) (daemon.Paths, error) {
	var err error
	abs := func(s string) string {
		if err != nil {
			return s
		}
		a, e := filepath.Abs(s)
		if e != nil {
			err = e
		}
		return a
	}

	p.ConfigFile = abs(p.ConfigFile)
	p.DataDir = abs(p.DataDir)
	p.LockFile = abs(p.LockFile)
	p.SocketPath = abs(p.SocketPath)
	return p, err
}
