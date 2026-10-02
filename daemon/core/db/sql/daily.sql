CREATE TABLE IF NOT EXISTS gpu_samples (
    ts INTEGER NOT NULL,
    gpu_id INTEGER NOT NULL,
    n INTEGER NOT NULL DEFAULT 1,
    util_gpu_sum INTEGER,
    util_gpu_max INTEGER,
    util_mem_sum INTEGER,
    temp_max INTEGER,
    power_mw_sum INTEGER,
    power_mw_max INTEGER,
    vram_used_max INTEGER,
    sm_clock_min INTEGER,
    mem_clock_min INTEGER,
    power_limit_mw INTEGER,
    throttle_or INTEGER,
    ecc_errors INTEGER,
    PRIMARY KEY (ts, gpu_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS agg (
    ts INTEGER NOT NULL,
    proc_id INTEGER NOT NULL,
    gpu_id INTEGER NOT NULL,
    launches INTEGER NOT NULL DEFAULT 0,
    memcpy_calls INTEGER NOT NULL DEFAULT 0,
    memcpy_bytes INTEGER NOT NULL DEFAULT 0,
    alloc_calls INTEGER NOT NULL DEFAULT 0,
    alloc_bytes INTEGER NOT NULL DEFAULT 0,
    free_bytes INTEGER NOT NULL DEFAULT 0,
    sync_calls INTEGER NOT NULL DEFAULT 0,
    sync_us_sum INTEGER NOT NULL DEFAULT 0,
    sync_us_max INTEGER NOT NULL DEFAULT 0,
    ioctl_calls INTEGER NOT NULL DEFAULT 0,
    uvm_faults INTEGER NOT NULL DEFAULT 0,
    uvm_evicts INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    vram_used_bytes INTEGER,
    PRIMARY KEY (ts, proc_id, gpu_id)
) WITHOUT ROWID;
