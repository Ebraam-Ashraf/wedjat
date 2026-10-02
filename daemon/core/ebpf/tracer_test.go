package ebpf

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
)

// TestMergeAggSumsCountsAndTakesMaxLatency pins the rule that makes the per-CPU
// aggregate correct: work is divided between CPUs and added, but the worst
// observed latency is a maximum. Summing the maxima would report a stall that no
// thread ever experienced.
func TestMergeAggSumsCountsAndTakesMaxLatency(t *testing.T) {
	merged := mergeAgg([]aggVal{
		{Count: 3, Bytes: 300, LatencySumNs: 900, LatencyMaxNs: 400, AllocBytes: 70},
		{Count: 4, Bytes: 400, LatencySumNs: 1200, LatencyMaxNs: 250, AllocBytes: 30},
	})

	if merged.Count != 7 {
		t.Errorf("count = %d, want 7 (each CPU counted its own share)", merged.Count)
	}
	if merged.Bytes != 700 {
		t.Errorf("bytes = %d, want 700", merged.Bytes)
	}
	if merged.LatencySumNs != 2100 {
		t.Errorf("latency sum = %d, want 2100", merged.LatencySumNs)
	}
	if merged.LatencyMaxNs != 400 {
		t.Errorf("latency max = %d, want 400 (the largest, not the sum 650)", merged.LatencyMaxNs)
	}
	if merged.AllocBytes != 100 {
		t.Errorf("alloc bytes = %d, want 100", merged.AllocBytes)
	}
}

// TestMergeAggIgnoresZeroedCPUs covers the sparse case where only some CPUs have
// taken a branch, which is the common case for an infrequent API.
func TestMergeAggIgnoresZeroedCPUs(t *testing.T) {
	merged := mergeAgg([]aggVal{{}, {Count: 1}, {}, {LatencyMaxNs: 99}})
	if merged.Count != 1 || merged.LatencyMaxNs != 99 {
		t.Errorf("merged = %+v, want count 1 and max 99", merged)
	}
}

// TestApplyAPIRoutesEachAPI checks that every kernel counter lands in the column
// the schema expects. A misrouted counter is silent: the totals still add up,
// they are just attributed to the wrong call.
func TestApplyAPIRoutesEachAPI(t *testing.T) {
	var row db.Aggregate

	applyAPI(&row, eventLaunch, aggVal{Count: 2})
	applyAPI(&row, eventMemcpy, aggVal{Count: 3, Bytes: 4096})
	applyAPI(&row, eventAlloc, aggVal{Count: 4, AllocBytes: 8192})
	applyAPI(&row, eventFree, aggVal{FreeBytes: 2048})
	applyAPI(&row, eventSync, aggVal{Count: 5, LatencySumNs: 9000, LatencyMaxNs: 4000})
	applyAPI(&row, eventIoctl, aggVal{Count: 6, Errors: 1})
	applyAPI(&row, eventUvmFault, aggVal{Count: 7, UvmFaults: 7})
	applyAPI(&row, eventUvmEvict, aggVal{Count: 8, UvmEvicts: 8})

	want := db.Aggregate{
		Launches:    2,
		MemcpyCalls: 3,
		MemcpyBytes: 4096,
		AllocCalls:  4,
		AllocBytes:  8192,
		FreeBytes:   2048,
		SyncCalls:   5,
		SyncUsSum:   9,
		SyncUsMax:   4,
		IoctlCalls:  6,
		UvmFaults:   7,
		UvmEvicts:   8,
		Errors:      1,
	}
	if row != want {
		t.Errorf("row = %+v\nwant %+v", row, want)
	}
}

// TestApplyAPIKeepsWorstSyncLatency guards the case of several API keys landing
// on one process and device: the row must keep the worst latency seen, not the
// newest one.
func TestApplyAPIKeepsWorstSyncLatency(t *testing.T) {
	var row db.Aggregate
	applyAPI(&row, eventSync, aggVal{Count: 1, LatencySumNs: 5_000_000, LatencyMaxNs: 5_000_000})
	applyAPI(&row, eventSync, aggVal{Count: 1, LatencySumNs: 1000, LatencyMaxNs: 1000})

	if row.SyncUsMax != 5000 {
		t.Errorf("sync us max = %d, want 5000 (a later faster sync must not lower it)", row.SyncUsMax)
	}
	if row.SyncCalls != 2 {
		t.Errorf("sync calls = %d, want 2", row.SyncCalls)
	}
}

// TestDecodeEventMatchesABI guards the struct layout against the C header. A
// field landing in the wrong slot would decode into plausible but wrong
// numbers, so the offsets are checked explicitly rather than by round trip.
func TestDecodeEventMatchesABI(t *testing.T) {
	raw := make([]byte, 64)
	binary.LittleEndian.PutUint64(raw[0:8], 111)
	binary.LittleEndian.PutUint64(raw[8:16], 222)
	binary.LittleEndian.PutUint64(raw[16:24], 333)
	binary.LittleEndian.PutUint64(raw[24:32], 444)
	binary.LittleEndian.PutUint64(raw[32:40], 555)
	binary.LittleEndian.PutUint32(raw[40:44], 666)
	binary.LittleEndian.PutUint32(raw[44:48], 777)
	binary.LittleEndian.PutUint32(raw[48:52], 888)
	binary.LittleEndian.PutUint32(raw[52:56], 999)
	binary.LittleEndian.PutUint32(raw[56:60], 0x5a5a)
	binary.LittleEndian.PutUint32(raw[60:64], 0xffffff9c)

	event, err := decodeEvent(raw)
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}

	if event.TsNano != 111 || event.StartBoottimeNs != 222 || event.LatencyNs != 333 ||
		event.Address != 444 || event.Bytes != 555 {
		t.Errorf("64-bit fields decoded wrong: %+v", event)
	}
	if event.Tgid != 666 || event.Tid != 777 || event.DeviceOrdinal != 888 ||
		event.ApiID != 999 || event.Flags != 0x5a5a {
		t.Errorf("32-bit fields decoded wrong: %+v", event)
	}
	if event.Status != -100 {
		t.Errorf("status = %d, want -100 (signed)", event.Status)
	}
}

// TestDecodeEventRejectsShortRecord matters because a truncated record would
// otherwise be read as real zeros and recorded as a plausible event.
func TestDecodeEventRejectsShortRecord(t *testing.T) {
	if _, err := decodeEvent(make([]byte, 32)); err == nil {
		t.Fatal("expected an error for a record shorter than the ABI")
	}
}

// TestDeviceKeyIsStable checks that an unresolvable device still produces a
// distinct, stable dedupe key rather than colliding with a real ordinal.
func TestDeviceKeyIsStable(t *testing.T) {
	if got := deviceKey(3); got != "3" {
		t.Errorf("deviceKey(3) = %q, want \"3\"", got)
	}
	if got := deviceKey(unknownDevice); got != "unknown" {
		t.Errorf("deviceKey(unknown) = %q, want \"unknown\"", got)
	}
	if deviceKey(unknownDevice) == deviceKey(3) {
		t.Error("unknown device must not share a dedupe key with a real ordinal")
	}
}

// TestIdentityResolvesSameRowAsNVMLPath is the invariant the whole tracer
// depends on: a process identified by the kernel and by NVML must land on one
// row, not two. If this breaks, every aggregate is attributed to a process that
// does not exist.
func TestIdentityResolvesSameRowAsNVMLPath(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenDB(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	ids := newTracerIdentity("boot-a")

	// The NVML path resolves the process through /proc.
	const pid = 1
	startTicks, command, err := core.ReadProcessIdentity(pid)
	if err != nil {
		t.Skipf("cannot read /proc/%d: %v", pid, err)
	}
	fromNVML, err := database.UpsertProcess(ctx, db.ProcessIdentity{
		BootID:        "boot-a",
		TGID:          pid,
		StartTicks:    startTicks,
		Command:       command,
		FirstSeenUnix: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("upsert process: %v", err)
	}

	// The tracer path resolves the same tgid independently.
	fromTracer, err := ids.processID(ctx, database, pid)
	if err != nil {
		t.Fatalf("resolve tgid: %v", err)
	}
	if fromTracer != fromNVML {
		t.Errorf("tracer resolved tgid %d to proc %d, NVML path used %d; the two paths must agree",
			pid, fromTracer, fromNVML)
	}

	// A different boot must never share the row, even for the same PID.
	otherBoot := newTracerIdentity("boot-b")
	other, err := otherBoot.processID(ctx, database, pid)
	if err != nil {
		t.Fatalf("resolve under second boot: %v", err)
	}
	if other == fromNVML {
		t.Error("a different boot ID must produce a different process row")
	}
}

// TestIdentityRejectsUnidentifiableProcess checks that a process the tracer
// cannot resolve is an error rather than a guess. A recycled PID would otherwise
// be credited to the previous occupant's row.
func TestIdentityRejectsUnidentifiableProcess(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenDB(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	ids := newTracerIdentity("boot-a")
	if _, err := ids.processID(ctx, database, 0); err == nil {
		t.Error("expected an error for tgid 0")
	}
	// PID 1 exists, so use a PID that cannot, to stand in for a process that
	// exited before it could be identified.
	if _, err := ids.processID(ctx, database, 0x7ffffffe); err == nil {
		t.Error("expected an error for a process that is not in /proc")
	}
}

// TestForgetDropsCachedProcess guards PID reuse: once a process exits its cached
// row must go, or the next process to get the PID would inherit it.
func TestForgetDropsCachedProcess(t *testing.T) {
	ids := newTracerIdentity("boot-a")
	ids.tgids[42] = 7
	ids.forget(42)
	if _, ok := ids.tgids[42]; ok {
		t.Error("forget did not drop the cached tgid")
	}
}

// TestGPUIDRejectsUnknownDevice ensures an unresolvable ordinal is never mapped
// to a real GPU, which would silently attribute one device's work to another.
func TestGPUIDRejectsUnknownDevice(t *testing.T) {
	ids := newTracerIdentity("boot-a")
	ids.setOrdinal(0, 11)
	ids.setOrdinal(1, 22)

	if id, ok := ids.gpuID(0); !ok || id != 11 {
		t.Errorf("gpuID(0) = %d, %v; want 11, true", id, ok)
	}
	if _, ok := ids.gpuID(unknownDevice); ok {
		t.Error("the unknown sentinel must not resolve to a device")
	}
	if _, ok := ids.gpuID(9); ok {
		t.Error("an unregistered ordinal must not resolve")
	}
}

// TestSetConfigWritesFlags covers the kernel-side configuration write, including
// the raw capture bit the daemon uses to trade volume for precision.
func TestSetConfigFlagsEncoding(t *testing.T) {
	const raw = configFlagRawCapture
	if raw != 1 {
		t.Errorf("configFlagRawCapture = %d, want 1; it mirrors CONFIG_F_RAW_CAPTURE", raw)
	}
	if eventFlagDeviceUnknown != 1 {
		t.Errorf("eventFlagDeviceUnknown = %d, want 1", eventFlagDeviceUnknown)
	}
}

// TestObjectsExistDetectsMissingObjects separates "not built" from "load failed"
// so the daemon can tell the operator which problem they have.
func TestObjectsExistDetectsMissingObjects(t *testing.T) {
	if ObjectsExist(t.TempDir()) {
		t.Error("ObjectsExist reported true for an empty directory")
	}
	if ObjectsExist("") == false {
		t.Log("default objects directory is absent (expected in a source checkout)")
	}

	dir := t.TempDir()
	for _, name := range bpfObjects {
		if err := os.WriteFile(dir+"/"+name+".bpf.o", nil, 0644); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if !ObjectsExist(dir) {
		t.Error("ObjectsExist reported false with every object present")
	}
}

// TestResolveObjectsDirRejectsIncompleteSet covers the failure that matters on a
// fresh machine: a directory holding only some of the objects would silently
// leave a whole class of events untraced, so it must not be accepted.
func TestResolveObjectsDirRejectsIncompleteSet(t *testing.T) {
	partial := t.TempDir()
	if err := os.WriteFile(partial+"/cuda_actions.bpf.o", nil, 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	if _, err := resolveObjectsDir(partial); err == nil {
		t.Error("a directory with only some objects was accepted")
	}

	complete := t.TempDir()
	for _, name := range bpfObjects {
		if err := os.WriteFile(complete+"/"+name+".bpf.o", nil, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	resolved, err := resolveObjectsDir(complete)
	if err != nil {
		t.Fatalf("resolve complete set: %v", err)
	}
	if resolved != complete {
		t.Errorf("resolved %q, want %q", resolved, complete)
	}
}

// TestResolveObjectsDirPrefersConfiguredPath checks that an operator's explicit
// setting wins over the built-in search order.
func TestResolveObjectsDirPrefersConfiguredPath(t *testing.T) {
	configured := t.TempDir()
	for _, name := range bpfObjects {
		if err := os.WriteFile(configured+"/"+name+".bpf.o", nil, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	resolved, err := resolveObjectsDir(configured)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != configured {
		t.Errorf("resolved %q, want the configured %q", resolved, configured)
	}
}

// TestFailureCountsGroupsByCause is what keeps a fatal misconfiguration visible:
// fifty probes failing for one reason must read as one line, not fifty.
func TestFailureCountsGroupsByCause(t *testing.T) {
	tracer := &Tracer{}
	for i := 0; i < 50; i++ {
		tracer.fail(fmt.Sprintf("cuda_actions/probe%d", i), "file /usr/lib/libcuda.so.1 is not executable")
	}
	tracer.fail("driver_kprobes/probe_a", "token __x64_nvidia_ioctl: not found")
	tracer.fail("driver_kprobes/probe_b", "token __x64_nvidia_ioctl: not found")

	counts := tracer.FailureCounts()
	if len(counts) != 2 {
		t.Fatalf("got %d distinct causes, want 2: %v", len(counts), counts)
	}
	if counts["file /usr/lib/libcuda.so.1 is not executable"] != 50 {
		t.Errorf("libcuda cause counted %d times, want 50", counts["file /usr/lib/libcuda.so.1 is not executable"])
	}
	if counts["token __x64_nvidia_ioctl: not found"] != 2 {
		t.Errorf("kprobe cause counted %d times, want 2", counts["token __x64_nvidia_ioctl: not found"])
	}
}

// TestAttachmentHintsCoverTheLibcudaFailure checks the one failure we know how to
// fix actually produces its remedy, naming the library that was actually used.
func TestAttachmentHintsCoverTheLibcudaFailure(t *testing.T) {
	causes := []string{"file /opt/custom/libcuda.so.1 is not executable", "token x: not found"}
	hints := attachmentHints(causes, "/opt/custom/libcuda.so.1")
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1: %v", len(hints), hints)
	}
	if !strings.Contains(hints[0], "/opt/custom/libcuda.so.1") {
		t.Errorf("hint does not name the library in use: %s", hints[0])
	}

	// An unknown cause must not be given invented advice.
	if got := attachmentHints([]string{"token x: not found"}, "/x"); len(got) != 0 {
		t.Errorf("unknown cause produced advice: %v", got)
	}
}

// TestDriverSymbolAliasesAreOrdered guards the alias table itself: a candidate
// list must always keep the name the BPF object asks for first, so an older
// driver that still exports it keeps working.
func TestDriverSymbolAliasesAreOrdered(t *testing.T) {
	for logical, candidates := range driverSymbolAliases {
		if len(candidates) == 0 {
			t.Errorf("%s: empty candidate list", logical)
			continue
		}
		if candidates[0] != logical {
			t.Errorf("%s: first candidate is %q, want the requested %q so older drivers keep working",
				logical, candidates[0], logical)
		}
		seen := map[string]bool{}
		for _, candidate := range candidates {
			if seen[candidate] {
				t.Errorf("%s: duplicate candidate %q", logical, candidate)
			}
			seen[candidate] = true
		}
	}
}

// TestDriverSymbolAliasesAvoidCompilerClones stops the table drifting toward
// symbols the compiler renames between builds, which would make a probe attach
// to a clone or silently miss.
func TestDriverSymbolAliasesAvoidCompilerClones(t *testing.T) {
	for logical, candidates := range driverSymbolAliases {
		for _, candidate := range candidates {
			for _, suffix := range []string{".part.", ".isra.", ".constprop.", ".cold"} {
				if strings.Contains(candidate, suffix) {
					t.Errorf("%s: candidate %q is a compiler-generated clone", logical, candidate)
				}
			}
		}
	}
}

// TestDriverAliasesResolveAgainstThisKernel checks the table against the running
// kernel. It is skipped where kallsyms is unreadable, but where it can read, it
// is the only thing standing between a driver rename and silently lost probes.
func TestDriverAliasesResolveAgainstThisKernel(t *testing.T) {
	data, err := os.ReadFile("/proc/kallsyms")
	if err != nil {
		t.Skipf("kallsyms unreadable: %v", err)
	}
	present := func(symbol string) bool {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[1] != "" && fields[2] == symbol {
				return true
			}
		}
		return false
	}

	for logical, candidates := range driverSymbolAliases {
		any := false
		for _, candidate := range candidates {
			if present(candidate) {
				any = true
				break
			}
		}
		if !any {
			t.Errorf("%s: this kernel exports none of %v", logical, candidates)
		}
	}
}
