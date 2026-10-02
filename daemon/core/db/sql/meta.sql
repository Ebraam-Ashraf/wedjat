CREATE TABLE IF NOT EXISTS gpus (
    gpu_id INTEGER PRIMARY KEY,
    uuid TEXT UNIQUE NOT NULL,
    idx INTEGER,
    name TEXT,
    pci_bus_id TEXT,
    vram_total_bytes INTEGER,
    driver_version TEXT,
    parent_gpu_id INTEGER REFERENCES gpus(gpu_id),
    first_seen_ts INTEGER NOT NULL,
    last_seen_ts INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS procs (
    proc_id INTEGER PRIMARY KEY AUTOINCREMENT,
    boot_id TEXT NOT NULL,
    tgid INTEGER NOT NULL,
    start_ticks INTEGER NOT NULL,
    command TEXT,
    cmdline TEXT,
    container TEXT,
    first_seen_ts INTEGER NOT NULL,
    end_ts INTEGER,
    end_reason TEXT,
    exit_code INTEGER,
    term_signal INTEGER,
    UNIQUE (boot_id, tgid, start_ticks)
);
CREATE INDEX IF NOT EXISTS procs_running ON procs(proc_id) WHERE end_ts IS NULL;
CREATE INDEX IF NOT EXISTS procs_end ON procs(end_ts);

CREATE TABLE IF NOT EXISTS proc_devices (
    proc_id INTEGER NOT NULL REFERENCES procs(proc_id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    gpu_id INTEGER NOT NULL REFERENCES gpus(gpu_id),
    PRIMARY KEY (proc_id, ordinal)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS proc_gpu (
    proc_id INTEGER NOT NULL REFERENCES procs(proc_id) ON DELETE CASCADE,
    gpu_id INTEGER NOT NULL REFERENCES gpus(gpu_id),
    first_seen_ts INTEGER NOT NULL,
    last_seen_ts INTEGER NOT NULL,
    peak_vram_bytes INTEGER,
    last_vram_bytes INTEGER,
    PRIMARY KEY (proc_id, gpu_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS dumps (
    dump_id INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_key TEXT NOT NULL,
    created_ts INTEGER NOT NULL,
    path TEXT NOT NULL,
    size_bytes INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS incidents (
    incident_id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    proc_id INTEGER REFERENCES procs(proc_id) ON DELETE SET NULL,
    gpu_id INTEGER REFERENCES gpus(gpu_id),
    first_ts INTEGER NOT NULL,
    last_ts INTEGER NOT NULL,
    occurrences INTEGER NOT NULL DEFAULT 1,
    dedupe_key TEXT NOT NULL,
    summary TEXT,
    detail TEXT,
    dump_id INTEGER REFERENCES dumps(dump_id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS incidents_time ON incidents(last_ts);
CREATE INDEX IF NOT EXISTS incidents_dedupe ON incidents(dedupe_key, last_ts);

CREATE TABLE IF NOT EXISTS daemon_log (
    ts INTEGER NOT NULL,
    level TEXT,
    kind TEXT,
    message TEXT
);
CREATE INDEX IF NOT EXISTS daemon_log_ts ON daemon_log(ts);

CREATE TABLE IF NOT EXISTS daemon_state (
    k TEXT PRIMARY KEY,
    v TEXT
) WITHOUT ROWID;
