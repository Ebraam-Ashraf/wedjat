// Wedjat CLI - GPU observability web interface and TUI.
// Default command runs the web server on 127.0.0.1:3000.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/assets/ascii"
	"github.com/Ebraam-Ashraf/wedjat/ui/server/httpd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Default paths (release paths)
	const (
		defaultDataDir    = "/var/lib/wedjat"
		defaultSocketPath = "/run/wedjat/wedjat.sock"
		defaultPort       = 3000
	)

	// Flags
	webFlag := flag.Bool("web", false, "Run web server (default)")
	tuiFlag := flag.Bool("tui", false, "Run TUI (not implemented yet)")
	uninstallFlag := flag.Bool("uninstall", false, "Run uninstall script")
	portFlag := flag.Int("port", defaultPort, "Web server port")
	socketFlag := flag.String("socket", defaultSocketPath, "Daemon socket path")
	dataFlag := flag.String("data", defaultDataDir, "Daemon data directory")
	noOpenFlag := flag.Bool("no-open", false, "Don't open browser")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	devFlag := flag.Bool("dev", false, "Use dev paths (prints dev command)")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `wedjat - GPU observability

Usage:
  sudo wedjat [flags]          # Run web server (default)
  sudo wedjat --web [flags]    # Run web server
  sudo wedjat --tui            # Run TUI (not implemented)
  sudo wedjat uninstall        # Uninstall wedjat (keeps data & config)
  sudo wedjat --version        # Print version

Flags:
`)
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), `
Defaults are release paths. For development, run:
  %s --dev
`, os.Args[0])
	}

	flag.Parse()

	// Handle --version
	if *versionFlag || flag.Arg(0) == "version" {
		fmt.Printf("wedjat %s (%s) built %s\n", version, commit, date)
		os.Exit(0)
	}

	// Handle uninstall (either via --uninstall or "uninstall" subcommand)
	if *uninstallFlag || flag.Arg(0) == "uninstall" {
		runUninstall()
		return
	}

	// Handle --tui
	if *tuiFlag {
		fmt.Println("TUI not implemented yet")
		os.Exit(1)
	}

	// Default to --web if no other mode specified
	if !*webFlag && !*tuiFlag && !*uninstallFlag && flag.Arg(0) != "uninstall" {
		*webFlag = true
	}

	// Handle --dev: print the dev command and exit
	if *devFlag {
		printDevCommand(*portFlag, *socketFlag, *dataFlag)
		return
	}

	// Print banner
	fmt.Print(ascii.Eye)
	fmt.Printf("\n\nwedjat %s\n\n", version)

	// Run web server
	runWebServer(*portFlag, *socketFlag, *dataFlag, *noOpenFlag)
}

func runWebServer(port int, socketPath, dataDir string, noOpen bool) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	cfg := httpd.Config{
		Addr:       addr,
		SocketPath: socketPath,
		DataDir:    dataDir,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpd.Serve(ctx, cfg)
	}()

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	// Print URL and try to open browser
	url := fmt.Sprintf("http://%s", addr)
	fmt.Printf("Web UI: %s\n", url)
	fmt.Printf("Daemon socket: %s\n", socketPath)
	fmt.Printf("Data directory: %s\n\n", dataDir)
	fmt.Println("Press Ctrl+C to stop")

	if !noOpen {
		openBrowser(url)
	}

	// Wait for server to exit
	if err := <-errCh; err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func openBrowser(url string) {
	// Try to run as SUDO_USER if available
	sudoUser := os.Getenv("SUDO_USER")
	var cmd *exec.Cmd

	if sudoUser != "" && runtime.GOOS == "linux" {
		// Run xdg-open as the original user
		cmd = exec.Command("sudo", "-u", sudoUser, "xdg-open", url)
	} else {
		// Fallback: try to open directly
		switch runtime.GOOS {
		case "linux":
			cmd = exec.Command("xdg-open", url)
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		default:
			log.Printf("Cannot open browser on %s", runtime.GOOS)
			return
		}
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}

// runUninstall is implemented in uninstall.go

func printDevCommand(port int, socketPath, dataDir string) {
	// Dev paths match wedjatd-dev
	devSocket := "./daemon/dev/run/wedjat.sock"
	devData := "./daemon/dev/var/lib/wedjat"

	fmt.Println("Development command:")
	fmt.Printf("  sudo -E ./wedjat --web --port=%d --socket=%s --data=%s\n\n", port, devSocket, devData)
	fmt.Println("Or run the dev daemon first:")
	fmt.Println("  make dev-daemon")
	fmt.Println("Then in another terminal:")
	fmt.Println("  make dev-ui")
	fmt.Println("Or run both:")
	fmt.Println("  make dev")
}