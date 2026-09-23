/*
 * Shared Wedjat eBPF/User-Space Contract
 *
 * Goal of this file:
 *   One place for event structs, map keys, API ids, and constants shared by
 *   Layer 1, Layer 2, Layer 3, and the user-space daemon/test loader.
 *
 * Why this matters:
 *   BPF C and user-space C must agree exactly on struct layout. If one side
 *   thinks comm is char[16] and the other thinks it is char, your logs become
 *   nonsense even if the probe fires correctly.
 */

/*
 * Common constants to define later:
 *
 *   WEDJAT_COMM_LEN = 16
 *   WEDJAT_UNKNOWN_DEVICE = 0xffffffff
 *   WEDJAT_SLOW_SYNC_NS = 10ms threshold
 *   WEDJAT_LARGE_ALLOC_BYTES = 100MB threshold
 */

/*
 * Event/API ids to define later:
 *
 *   API_CUDA_LAUNCH
 *   API_CUDA_MEM_ALLOC
 *   API_CUDA_MEMCPY
 *   API_CUDA_STREAM_SYNC
 *   API_CUDA_CTX_SET
 *   API_DRIVER_IOCTL
 *   API_UVM_FAULT
 *   API_UVM_MIGRATE
 *   API_UVM_EVICT
 */

/*
 * Layer 1 identity struct should contain:
 *
 *   u64 timestamp_ns;
 *   u64 cgroup_id;
 *   u64 uid;
 *   u64 gid;
 *   u32 pid;
 *   u32 tid;
 *   u32 cpu_id;
 *   u32 device_id;
 *   char comm[16];
 */

/*
 * Aggregation key should contain:
 *
 *   pid / cgroup_id / api_id / device_id
 *
 * Keep key fields stable and small. This map will be hit often.
 */

/*
 * Aggregation value should contain:
 *
 *   call_count
 *   total_bytes
 *   total_latency_ns
 *   error_count
 *   max_latency_ns maybe later
 */

/*
 * Ringbuf event should contain:
 *
 *   identity envelope from Layer 1
 *   event_type / api_id
 *   error_code
 *   bytes
 *   latency_ns
 *   stream handle maybe
 *   small union for launch/memcpy/driver details later
 *
 * Rule:
 *   Ringbuf is for rare/dev/notable events. Do not emit every kernel launch in
 *   production unless you intentionally want a firehose.
 */
