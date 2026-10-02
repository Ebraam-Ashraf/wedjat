package core_test

import (
	"os"
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
)

func TestReadProcessIdentityForCurrentProcess(t *testing.T) {
	startTicks, command, err := core.ReadProcessIdentity(uint(os.Getpid()))
	if err != nil {
		t.Fatalf("read current process identity: %v", err)
	}
	if startTicks <= 0 || command == "" {
		t.Fatalf("identity = start_ticks %d, command %q; want populated values", startTicks, command)
	}
}
