package core

import (
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

func drain(ch chan []byte) []string {
	var out []string
	for {
		select {
		case m := <-ch:
			out = append(out, string(m))
		default:
			return out
		}
	}
}

// TestSendToClientQueuesWhenThereIsRoom verifies the ordinary path is a plain
// non-blocking enqueue.
func TestSendToClientQueuesWhenThereIsRoom(t *testing.T) {
	source.Dropped.Store(0)
	ch := make(chan []byte, 4)

	sendToClient(ch, []byte("a"))
	sendToClient(ch, []byte("b"))

	got := drain(ch)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("queue = %v, want [a b]", got)
	}
	if source.Dropped.Load() != 0 {
		t.Fatalf("Dropped = %d, want 0", source.Dropped.Load())
	}
}

// TestSendToClientDropsOldestNotNewest is the behavioural fix: when a client
// falls behind, the stale message is the one discarded, so the client receives
// the most recent telemetry instead of a backlog it can never catch up on.
func TestSendToClientDropsOldestNotNewest(t *testing.T) {
	source.Dropped.Store(0)
	ch := make(chan []byte, 2)

	sendToClient(ch, []byte("a"))
	sendToClient(ch, []byte("b"))
	sendToClient(ch, []byte("c"))

	got := drain(ch)
	if len(got) != 2 {
		t.Fatalf("queue length = %d, want 2 (capacity preserved)", len(got))
	}
	if got[0] != "b" || got[1] != "c" {
		t.Fatalf("queue = %v, want [b c]: the oldest message must be evicted", got)
	}
	if source.Dropped.Load() != 1 {
		t.Fatalf("Dropped = %d, want 1", source.Dropped.Load())
	}
}

// TestSendToClientKeepsNewestUnderSustainedOverload verifies that a client which
// never drains still converges on current data rather than being frozen in the
// past.
func TestSendToClientKeepsNewestUnderSustainedOverload(t *testing.T) {
	source.Dropped.Store(0)
	ch := make(chan []byte, 2)

	for i := 0; i < 50; i++ {
		sendToClient(ch, []byte{byte('a' + i%26)})
	}

	got := drain(ch)
	if len(got) != 2 {
		t.Fatalf("queue length = %d, want 2", len(got))
	}
	// 50 sends of 'a'+i%26, so the final two are i=48 -> 'w' and i=49 -> 'x'.
	if got[0] != "w" || got[1] != "x" {
		t.Fatalf("queue = %v, want [w x]: the client must end up on the newest data", got)
	}
	// 50 sends into a queue of 2 evicts 48 messages.
	if source.Dropped.Load() != 48 {
		t.Fatalf("Dropped = %d, want 48", source.Dropped.Load())
	}
}

// TestSendToClientDoesNotBlockForever verifies the broadcaster cannot be stalled
// by a client that never reads.
func TestSendToClientDoesNotBlockForever(t *testing.T) {
	source.Dropped.Store(0)
	ch := make(chan []byte, perClientBufSize)
	for i := 0; i < perClientBufSize; i++ {
		sendToClient(ch, []byte("x"))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			sendToClient(ch, []byte("x"))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sendToClient blocked on a client that never reads")
	}
}
