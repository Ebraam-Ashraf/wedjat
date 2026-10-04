package ebpf

// aggregateBacklog is kept as a zero-size stub so the Tracer struct field
// compiles. The new drain path (Session.drainAggregates) does not use a
// backlog; it sends directly to source.Chans. The field in Tracer cannot be
// removed because tracer.go is not modified in this refactor.
type aggregateBacklog struct{}
