package source_test

import (
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// TestNewChans verifies all channels are initialised.
func TestNewChans(t *testing.T) {
	c := source.NewChans(4)
	if c.GPU == nil || c.Procs == nil || c.Xid == nil || c.Agg == nil || c.Event == nil {
		t.Fatal("NewChans: one or more channels are nil")
	}
}

// TestSendNonBlocking verifies that Send never blocks even when the channel is full,
// and that it increments the Dropped counter.
func TestSendNonBlocking(t *testing.T) {
	source.Dropped.Store(0)
	ch := make(chan int, 1)
	ch <- 99 // fill it

	start := time.Now()
	source.Send(ch, 1)
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("Send blocked on a full channel")
	}
	if source.Dropped.Load() != 1 {
		t.Fatalf("Dropped = %d, want 1", source.Dropped.Load())
	}
}

// TestSendDeliversWhenCapacityAvailable checks the normal (non-dropped) path.
func TestSendDeliversWhenCapacityAvailable(t *testing.T) {
	source.Dropped.Store(0)
	ch := make(chan int, 1)
	source.Send(ch, 42)

	select {
	case v := <-ch:
		if v != 42 {
			t.Fatalf("got %d, want 42", v)
		}
	default:
		t.Fatal("channel is empty after Send")
	}
	if source.Dropped.Load() != 0 {
		t.Fatalf("Dropped = %d, want 0", source.Dropped.Load())
	}
}

// TestClientsCountUpDown verifies that Clients can be incremented and decremented.
func TestClientsCountUpDown(t *testing.T) {
	source.Clients.Store(0)

	source.Clients.Add(1)
	if source.Clients.Load() != 1 {
		t.Fatalf("Clients = %d, want 1", source.Clients.Load())
	}

	source.Clients.Add(1)
	if source.Clients.Load() != 2 {
		t.Fatalf("Clients = %d, want 2", source.Clients.Load())
	}

	source.Clients.Add(-1)
	if source.Clients.Load() != 1 {
		t.Fatalf("Clients = %d, want 1", source.Clients.Load())
	}

	source.Clients.Add(-1)
	if source.Clients.Load() != 0 {
		t.Fatalf("Clients = %d, want 0", source.Clients.Load())
	}
}

// TestSendGPUSample verifies Send works with GPUSample slices.
func TestSendGPUSample(t *testing.T) {
	source.Dropped.Store(0)
	c := source.NewChans(8)
	gpus := []source.GPUSample{{UUID: "GPU-abc", Valid: true}}
	source.Send(c.GPU, gpus)

	select {
	case got := <-c.GPU:
		if len(got) != 1 || got[0].UUID != "GPU-abc" {
			t.Fatalf("unexpected value: %+v", got)
		}
	default:
		t.Fatal("GPU channel empty after Send")
	}
}

// TestSendAggRow verifies Send works with AggRow slices.
func TestSendAggRow(t *testing.T) {
	source.Dropped.Store(0)
	c := source.NewChans(8)
	rows := []source.AggRow{{Tgid: 1234, Count: 5}}
	source.Send(c.Agg, rows)

	select {
	case got := <-c.Agg:
		if len(got) != 1 || got[0].Tgid != 1234 {
			t.Fatalf("unexpected value: %+v", got)
		}
	default:
		t.Fatal("Agg channel empty after Send")
	}
}

// TestSendEvent verifies Send works with Events.
func TestSendEvent(t *testing.T) {
	source.Dropped.Store(0)
	c := source.NewChans(8)
	ev := source.Event{Tgid: 999, ApiID: 17}
	source.Send(c.Event, ev)

	select {
	case got := <-c.Event:
		if got.Tgid != 999 || got.ApiID != 17 {
			t.Fatalf("unexpected value: %+v", got)
		}
	default:
		t.Fatal("Event channel empty after Send")
	}
}
