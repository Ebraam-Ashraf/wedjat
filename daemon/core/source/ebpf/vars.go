package ebpf

import "sync/atomic"

// malformed counts records that could not be decoded. A non-zero value means
// the built BPF objects and this binary disagree about the event layout.
var malformed atomic.Uint64

// unattributed counts events the kernel recorded but that cannot be attributed
// to a GPU. Referenced by Tracer.Stats().
var unattributed atomic.Int64
