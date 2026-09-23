#ifndef COMMON_H
#define COMMON_H

#define COMM_LEN 16
#define UNKNOWN_DEVICE 0xffffffffU
#define SLOW_SYNC_NS (10ULL * 1000ULL * 1000ULL)
#define LARGE_ALLOC_BYTES (100ULL * 1024ULL * 1024ULL)

enum api_id {
    API_CUDA_LAUNCH = 1,
    API_CUDA_MEM_ALLOC = 2,
    API_CUDA_MEMCPY = 3,
    API_CUDA_MEMCPY_HTOD = 4,
    API_CUDA_MEMCPY_DTOH = 5,
    API_CUDA_MEMCPY_DTOD = 6,
    API_CUDA_STREAM_SYNC = 7,
    API_CUDA_CTX_SET = 8,
    API_DRIVER_IOCTL = 10,
    API_UVM_FAULT = 11,
    API_UVM_MIGRATE = 12,
    API_UVM_EVICT = 13,
    API_PREEMPT = 14,
};

struct identity {
    u64 timestamp_ns;
    u64 cgroup_id;
    u64 uid;
    u64 gid;
    u32 pid;
    u32 tid;
    u32 cpu_id;
    u32 device_id;
    char comm[COMM_LEN];
};

struct launch_event {
    u64 timestamp_ns;
    u32 pid;
    u32 tid;
    char comm[COMM_LEN];
    u32 grid_x;
    u32 grid_y;
    u32 grid_z;
    u32 block_x;
    u32 block_y;
    u32 block_z;
    u32 shared_mem_bytes;
    u64 stream;
    u64 function;
};

struct action_event {
    u64 timestamp_ns;
    u64 latency_ns;
    u64 bytes;
    u64 devptr;
    u32 pid;
    u32 tid;
    u32 api_id;
    u32 result;
    char comm[COMM_LEN];
};

struct agg_key {
    u64 cgroup_id;
    u32 pid;
    u32 api_id;
    u32 device_id;
};

struct agg_value {
    u64 call_count;
    u64 total_bytes;
    u64 total_latency_ns;
    u64 error_count;
    u64 max_latency_ns;
};

#endif
