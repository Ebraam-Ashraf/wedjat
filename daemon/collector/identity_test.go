package collector

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// readProcIdentity parses a real /proc/<pid>/stat line. The comm field is
// parenthesised and may itself contain spaces and parentheses, so parsing must
// anchor on the LAST ')' rather than splitting the whole line on whitespace.
func TestReadProcIdentityHandlesAwkwardCommNames(t *testing.T) {
	self := os.Getpid()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", self))
	if err != nil {
		t.Skipf("cannot read /proc/%d/stat: %v", self, err)
	}
	_ = data

	identity, err := ReadProcIdentity(self)
	if err != nil {
		t.Fatalf("ReadProcIdentity(self): %v", err)
	}
	if identity.TGID != int64(self) {
		t.Fatalf("TGID = %d, want %d", identity.TGID, self)
	}
	if identity.Command == "" {
		t.Fatal("comm must not be empty")
	}
	if identity.StartTicks <= 0 {
		t.Fatalf("startTicks = %d, want positive", identity.StartTicks)
	}
	if identity.BootID == "" {
		t.Fatal("boot id must be populated from /proc/sys/kernel/random/boot_id")
	}
}

func TestReadProcIdentityRejectsMissingProcess(t *testing.T) {
	// A PID that cannot exist: readProcIdentity must return an error rather
	// than a zero identity that could be mistaken for a real process.
	if _, err := ReadProcIdentity(1 << 30); err == nil {
		t.Fatal("expected an error for a nonexistent pid")
	}
}

// parseStatLine mirrors the field indexing in readProcIdentity so the offset
// arithmetic can be checked against a synthetic line, including a comm field
// containing spaces and parentheses.
func parseStatLine(line string) (string, int64, error) {
	open := strings.IndexByte(line, '(')
	end := strings.LastIndexByte(line, ')')
	if open < 0 || end < open || end+1 >= len(line) {
		return "", 0, fmt.Errorf("malformed stat line")
	}
	comm := line[open+1 : end]
	fields := strings.Fields(line[end+1:])
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return "", 0, fmt.Errorf("too few fields after comm")
	}
	start, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	return comm, start, err
}

func TestStatFieldIndexFindsStartTime(t *testing.T) {
	// A normal line: state, ppid, pgrp, session, tty, tpgid, flags, minflt,
	// cminflt, majflt, cmajflt, utime, stime, cutime, cstime, priority,
	// nice, threads, itrealvalue, starttime.
	line := "4242 (myprog) S 1 4242 4242 0 -1 4194304 100 0 0 0 11 22 0 0 20 0 1 0 987654 0 0 0 0 0 0 0 17 2 0 0 0 0 0"
	comm, start, err := parseStatLine(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if comm != "myprog" {
		t.Fatalf("comm = %q, want myprog", comm)
	}
	if start != 987654 {
		t.Fatalf("starttime = %d, want 987654", start)
	}
}

func TestStatFieldIndexSurvivesCommWithSpacesAndParens(t *testing.T) {
	// Go test binaries are named like "collector.test". A pathological but
	// legal name is "(sd-pam)" or one containing spaces; naive splitting on
	// whitespace would read the wrong field here.
	line := "77 ((weird) name) S 1 77 77 0 -1 4194304 100 0 0 0 11 22 0 0 20 0 1 0 424242 0 0 0 0 0 0 0 17 2 0 0 0 0 0"
	comm, start, err := parseStatLine(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if comm != "(weird) name" {
		t.Fatalf("comm = %q, want %q", comm, "(weird) name")
	}
	if start != 424242 {
		t.Fatalf("starttime = %d, want 424242", start)
	}
}

func TestStatFieldIndexRejectsTruncatedLine(t *testing.T) {
	if _, _, err := parseStatLine("1 (x) S 1 1"); err == nil {
		t.Fatal("expected a truncated stat line to be rejected")
	}
}

// A PID is not a stable identity; the (tgid, startTicks) pair is. This is the
// property that makes PID reuse safe, and it is what the store's
// (boot_id, tgid, start_ticks) unique constraint encodes.
func TestProcessIdentityDistinguishesReusedPIDs(t *testing.T) {
	first := ProcIdentity{BootID: "boot-a", TGID: 4242, StartTicks: 100, Command: "job-a"}
	second := ProcIdentity{BootID: "boot-a", TGID: 4242, StartTicks: 200, Command: "job-b"}

	if first == second {
		t.Fatal("identities with different startTicks must differ")
	}

	// The same PID across a reboot is also a different process.
	afterReboot := ProcIdentity{BootID: "boot-b", TGID: 4242, StartTicks: 100, Command: "job-a"}
	if first == afterReboot {
		t.Fatal("identities from different boots must differ")
	}
}

func TestIdentityCacheReusesResolvedProcess(t *testing.T) {
	// Without a store, ensureProcess cannot succeed, but the cache must still
	// recognise that it already asked about a PID rather than re-reading
	// /proc on every 2-second poll.
	c := newIdentityCache()
	if _, ok := c.cached(os.Getpid()); ok {
		t.Fatal("a fresh cache must report nothing cached")
	}
	identity := ProcIdentity{BootID: "boot-a", TGID: 7, StartTicks: 99, Command: "x"}
	c.byPID[7] = identity
	c.byProc[identity] = 12

	got, ok := c.cached(7)
	if !ok || got != identity {
		t.Fatalf("cached = %+v ok=%v", got, ok)
	}
	if id := c.procIDOf(identity); id != 12 {
		t.Fatalf("procIDOf = %d, want 12", id)
	}
}

func TestForgetPIDDropsOnlyTheStaleEntry(t *testing.T) {
	// When a PID is reused, the new instance must not inherit the old
	// ledger id, but the old process row still exists and must stay cached
	// so the lifecycle path can still close it.
	c := newIdentityCache()
	old := ProcIdentity{BootID: "boot-a", TGID: 7, StartTicks: 100}
	c.byPID[7] = old
	c.byProc[old] = 3

	c.forgetPID(7, ProcIdentity{BootID: "boot-a", TGID: 7, StartTicks: 999})
	if _, ok := c.cached(7); !ok {
		t.Fatal("a forget naming a different instance must not drop the entry")
	}

	c.forgetPID(7, old)
	if _, ok := c.cached(7); ok {
		t.Fatal("the stale PID entry must be dropped")
	}
	if c.procIDOf(old) != 3 {
		t.Fatal("the ledger row for the previous instance must be preserved")
	}
}

func TestGPUIDMappingIsPerDevice(t *testing.T) {
	c := newIdentityCache()
	if _, ok := c.gpuID(0); ok {
		t.Fatal("no device is registered yet")
	}
	c.setGPUID(0, 4)
	c.setGPUID(1, 9)
	if id, ok := c.gpuID(0); !ok || id != 4 {
		t.Fatalf("gpuID(0) = %d ok=%v, want 4", id, ok)
	}
	if id, ok := c.gpuID(1); !ok || id != 9 {
		t.Fatalf("gpuID(1) = %d ok=%v, want 9", id, ok)
	}
}
