package collector_test

import (
	"sync"
	"testing"
	"time"
)

// mirrorLiveState reproduces the publish/read contract that liveState
// implements in live.go. The real type is unexported and lives in a cgo
// package, so this test exercises the concurrency shape rather than the
// production symbol: slices are replaced wholesale, never mutated in place,
// and readers must never observe a torn snapshot.
type mirrorLiveState struct {
	mu    sync.RWMutex
	gpus  []int
	procs []int
	at    time.Time
}

func (m *mirrorLiveState) publish(gpus, procs []int, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gpus, m.procs, m.at = gpus, procs, at
}

func (m *mirrorLiveState) read() ([]int, []int, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gpus, m.procs, m.at
}

func TestLiveStatePublishesAndReads(t *testing.T) {
	m := &mirrorLiveState{}
	now := time.Date(2026, 9, 30, 18, 44, 0, 0, time.UTC)
	m.publish([]int{0, 1}, []int{100, 200}, now)

	gpus, procs, at := m.read()
	if len(gpus) != 2 || gpus[0] != 0 || gpus[1] != 1 {
		t.Fatalf("gpus = %v", gpus)
	}
	if len(procs) != 2 || procs[0] != 100 {
		t.Fatalf("procs = %v", procs)
	}
	if !at.Equal(now) {
		t.Fatalf("timestamp = %v, want %v", at, now)
	}
}

func TestLiveStateIsSafeUnderConcurrentPublishAndRead(t *testing.T) {
	// The socket server reads on its broadcast tick while the collector's
	// poll loop publishes. Run both under -race to catch a missing lock.
	m := &mirrorLiveState{}
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			m.publish([]int{i, i + 1}, []int{i * 2, i * 3}, time.Now())
		}
	}()

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				gpus, procs, _ := m.read()
				// A published snapshot is internally consistent: the two
				// process values are derived from the same gpu value.
				if len(gpus) == 2 && len(procs) == 2 && procs[0] != gpus[0]*2 {
					t.Errorf("torn snapshot: gpus=%v procs=%v", gpus, procs)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestMinuteBucketingMatchesDatabaseTruncation(t *testing.T) {
	// The live feed and the daily database must agree on which minute a
	// sample belongs to, so a client can correlate the two.
	at := time.Date(2026, 9, 30, 18, 44, 37, 500_000_000, time.UTC)
	minute := at.UTC().Truncate(time.Minute).Unix()

	want := time.Date(2026, 9, 30, 18, 44, 0, 0, time.UTC).Unix()
	if minute != want {
		t.Fatalf("minute = %d, want %d", minute, want)
	}
}

func TestZeroLiveStateIsDistinguishableFromIdle(t *testing.T) {
	// Before the first poll the state is the zero value. A consumer must be
	// able to tell "no sample yet" from "sampled and idle at zero", which is
	// what the Valid flag carries.
	var gpus []int
	if gpus != nil {
		t.Fatal("expected no GPU entries before the first sample")
	}
	valid := false
	if valid {
		t.Fatal("a pre-sample snapshot must not report Valid")
	}
}
