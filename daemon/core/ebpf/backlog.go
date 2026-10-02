package ebpf

import (
	"context"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
)

// aggregateBacklog queues batches of aggregate rows that could not be written
// on the drain tick that produced them and retries them on the next tick.
//
// The zero value is ready to use. Tracer holds one by value, so there is no
// constructor.
//
// Queue discipline: batches are written oldest first. A batch that fails stays
// at the head of the queue and is retried next tick. The queue is capped at
// maxBacklogBatches; when the cap is hit the oldest batch is dropped to make
// room, because rows that age past the minute boundary they belong to would
// fold into a different minute's row on write, which is more misleading than
// losing them.
const maxBacklogBatches = 60 // one minute of one-second drain ticks

type backlogBatch struct {
	at   time.Time
	rows []db.Aggregate
}

type aggregateBacklog struct {
	queue []backlogBatch
}

// Add enqueues a batch. If the queue is at capacity the oldest batch is
// dropped to keep memory bounded.
func (b *aggregateBacklog) Add(at time.Time, rows []db.Aggregate) {
	if len(rows) == 0 {
		return
	}
	if len(b.queue) >= maxBacklogBatches {
		// Drop the oldest batch.
		b.queue = b.queue[1:]
	}
	b.queue = append(b.queue, backlogBatch{at: at, rows: rows})
}

// Flush calls write for every queued batch in arrival order. On the first
// write error it stops and returns that error, leaving the failed batch and
// all later batches in the queue for the next call.
func (b *aggregateBacklog) Flush(ctx context.Context, write func(context.Context, time.Time, []db.Aggregate) error) error {
	for len(b.queue) > 0 {
		head := b.queue[0]
		if err := write(ctx, head.at, head.rows); err != nil {
			return err
		}
		b.queue = b.queue[1:]
	}
	return nil
}
