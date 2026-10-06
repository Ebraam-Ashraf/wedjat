# Wedjat development UI

The development UI is a single-host NVIDIA GPU monitor. Run it with the
development daemon and the commands in [run.md](./run.md).

## Pages and data feeds

- **Live (`/`)** — live `gpu` and `procs` socket messages, GPU identity and
  total VRAM from `/api/gpus`. Live graphs use daemon timestamps on a sliding
  60-second axis; freshness text updates once per second and sample dots pulse
  only when a `gpu` message arrives.
- **GPU detail (`/gpu/:id`)** — live GPU samples and processes, plus identity
  metadata from `/api/gpus`.
- **Processes (`/processes`)** — live `procs` and `agg` messages, enriched by
  `/api/processes` and `/api/aggregates`.
- **Events (`/events`)** — live `xid` and `event` socket messages, current
  throttle bitmasks from GPU samples, and `/api/incidents`.
- **History (`/history`)** — `/api/history` minute buckets.
- **Global status** — `/api/status` every five seconds and the browser socket
  connection state. GPU telemetry is marked stale after two seconds without a
  `gpu` message.

## Snapshot behavior

- History, incidents, process metadata and stored aggregates show their last
  successful fetch time and a manual Refresh button.
- History and stored process aggregates are minute-bucket data. History refreshes
  every 20 seconds, incidents and stored aggregates every 15 seconds, and process
  metadata every five seconds while their page is mounted. Timers stop on navigation.
- Implementation order: record daemon sample timestamps and per-GPU freshness in
  the shared store before building the Live page, so its first charts and cards
  use the same liveness contract as later pages.

## Known limits

- The daemon sends no snapshot on socket connect. Live panels start empty and
  fill when messages arrive; switching pages preserves the app-level store.
- The daemon does not report per-SM activity or SM count. The Live page only
  draws an explicitly **estimated** active/idle grid for GPU variants whose
  SM count can be identified from the GPU name and VRAM size. Other models show
  the whole-GPU utilization bar and an unavailable SM count.
- The expandable architecture illustration is generic and static. It is not a
  live diagram of the installed GPU.
- Live aggregate/event CUDA ordinals are process-visible ordinals. They do not
  always map reliably to a physical GPU in the browser, so the UI labels them
  as CUDA ordinals and does not claim a physical-device mapping.
- Socket delivery can drop old messages when a client falls behind. Sparklines
  therefore show received samples, not a complete sample history. The graph holds
  the last received value to the current time only while that sample is fresh; it
  does not fabricate additional measurements.
- The history endpoint returns at most 100 rows from today's UTC database,
  across GPUs. The daemon retains daily database files for 30 days by default,
  but this page does not query prior days. Historical power is the endpoint's
  per-minute sum of valid power readings, not an instantaneous sample.
- The daemon has no persisted throttle-event history. Events shows current
  throttle reasons from the latest live sample; incident history contains Xid
  and CUDA sync incidents.
- Throttle reason names follow the NVML bitmask values. Unknown bits are shown
  in hexadecimal so newly introduced values are not silently discarded.
