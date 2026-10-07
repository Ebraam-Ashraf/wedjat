package main

import (
	"log"

	"github.com/Ebraam-Ashraf/wedjat/internal/daemon"
)

var paths = daemon.Paths{
	ConfigFile:  "/etc/wedjat/config.yaml",
	DataDir:     "/var/lib/wedjat",
	LockFile:    "/run/wedjat/daemon.lock",
	SocketPath:  "/run/wedjat/wedjat.sock",
	SocketGroup: "wedjat",
}

func main() {
	log.Println("wedjatd starting (production mode)")
	if err := daemon.Run(paths, "wedjatd is running", nil); err != nil {
		log.Fatalf("FATAL: %v", err)
	}
}
