package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

const maxRetryRows = 100_000

var unattributed atomic.Uint64

type processCache struct {
	key        procKey
	identity   ProcessIdentity
	ordinals   map[uint32]int64
	resolved   bool
	exitQueued bool
}

type timedGPU struct {
	minute int64
	sample GPUMinuteSample
}
type pendingVRAM struct {
	key procKey
	row ProcessVRAM
}
type pendingAggregate struct {
	key    procKey
	minute int64
	row    Aggregate
}
type aggGroupKey struct {
	key           procKey
	gpuID, minute int64
}

type processEnd struct {
	key      procKey
	procID   int64
	endTS    int64
	reason   string
	exitCode *int
	termSig  *int
}

// Batch is the complete unit folded by the DB consumer before a flush.
type Batch struct {
	GPUMinutes  []timedGPU
	Processes   []ProcessIdentity
	ProcVRAM    []pendingVRAM
	Aggregates  []pendingAggregate
	Incidents   []Incident
	ProcessEnds []processEnd
	Seen        []procKey
	Sweep       bool
	HasProcList bool
}

func (b Batch) rows() int {
	return len(b.GPUMinutes) + len(b.Processes) + len(b.ProcVRAM) + len(b.Aggregates) + len(b.Incidents) + len(b.ProcessEnds) + len(b.Seen)
}

// Layer is the single DB consumer. Its event loop owns all folding state.
type Layer struct {
	database     *DB
	bootID       string
	devices      []gpuIdentity
	processCache map[uint32]processCache
	batch        Batch
	stop         context.CancelFunc
	stopped      chan struct{}
	writeBatch   func(context.Context, Batch) error
	readIdentity func(uint) (int64, string, error)
	readEnviron  func(uint32) ([]byte, error)
	stopErr      error
}

func StartLayer(ctx context.Context, database *DB, bootID string, devices []source.DeviceInfo, dbc source.Chans) *Layer {
	ids := make([]gpuIdentity, 0, len(devices))
	for _, device := range devices {
		id, err := database.GPUIDByUUID(ctx, device.UUID)
		if err != nil {
			log.Printf("Layer: failed to resolve GPU %s: %v", device.UUID, err)
			continue
		}
		ids = append(ids, gpuIdentity{device: device, id: id})
	}
	layerCtx, cancel := context.WithCancel(ctx)
	l := &Layer{database: database, bootID: bootID, devices: ids,
		processCache: make(map[uint32]processCache), stop: cancel, stopped: make(chan struct{}),
		readIdentity: ReadProcessIdentity, readEnviron: readProcessEnviron}
	l.writeBatch = l.WriteBatch
	go l.run(layerCtx, dbc)
	return l
}

func (l *Layer) Stop() error {
	l.stop()
	select {
	case <-l.stopped:
		return l.stopErr
	case <-time.After(6 * time.Second):
		return errors.New("layer shutdown timeout")
	}
}

func (l *Layer) run(ctx context.Context, dbc source.Chans) {
	defer close(l.stopped)
	flushTicker := time.NewTicker(time.Second)
	defer flushTicker.Stop()
	heartbeatTicker := time.NewTicker(10 * time.Second)
	defer heartbeatTicker.Stop()
	var lastUnattributed uint64
	lastDropped := source.Dropped.Load()
	for {
		select {
		case <-ctx.Done():
			l.drainPending(dbc)
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			l.stopErr = l.flush(flushCtx)
			cancel()
			if l.stopErr != nil {
				log.Printf("Layer: final flush failed: %v", l.stopErr)
			}
			return
		case gpus := <-dbc.GPU:
			l.handleGPUs(gpus)
		case procs := <-dbc.Procs:
			l.handleProcs(procs)
		case rows := <-dbc.Agg:
			l.handleAggregates(rows)
		case event := <-dbc.Event:
			l.handleEvent(event)
		case xid := <-dbc.Xid:
			l.handleXid(xid)
		case <-flushTicker.C:
			if err := l.flush(ctx); err != nil {
				log.Printf("Layer: flush failed: %v", err)
			}
		case <-heartbeatTicker.C:
			if err := l.database.WriteHeartbeat(ctx); err != nil {
				log.Printf("Layer: heartbeat failed: %v", err)
			}
			current := unattributed.Load()
			if current > lastUnattributed {
				log.Printf("Warning: %d new eBPF aggregate row(s) could not be attributed", current-lastUnattributed)
			}
			lastUnattributed = current
			dropped := source.Dropped.Load()
			if dropped > lastDropped { log.Printf("Warning: source channels/retry queue dropped %d telemetry row(s)", dropped-lastDropped) }
			lastDropped = dropped
		}
	}
}

func (l *Layer) drainPending(dbc source.Chans) {
	for {
		select {
		case gpus := <-dbc.GPU:
			l.handleGPUs(gpus)
		case procs := <-dbc.Procs:
			l.handleProcs(procs)
		case rows := <-dbc.Agg:
			l.handleAggregates(rows)
		case event := <-dbc.Event:
			l.handleEvent(event)
		case xid := <-dbc.Xid:
			l.handleXid(xid)
		default:
			return
		}
	}
}

func (l *Layer) handleGPUs(gpus []source.GPUSample) {
	for _, gpu := range gpus {
		if !gpu.Valid {
			continue
		}
		gpuID := l.gpuID(gpu.UUID)
		if gpuID < 0 {
			continue
		}
		v := gpu.ValidFields
		minute := gpu.TsNano / int64(time.Minute) * int64(time.Minute) / int64(time.Second)
		l.batch.GPUMinutes = append(l.batch.GPUMinutes, timedGPU{minute: minute, sample: GPUMinuteSample{
			GPUID:      gpuID,
			UtilGPUSum: reported(v&source.ValidGPUUtil != 0, int64(gpu.UtilGPU)), UtilGPUMax: reported(v&source.ValidGPUUtil != 0, int64(gpu.UtilGPU)),
			UtilMemSum: reported(v&source.ValidMemUtil != 0, int64(gpu.UtilMem)), TempC: reported(v&source.ValidTemp != 0, int64(gpu.TempC)),
			PowerMW: reported(v&source.ValidPower != 0, int64(gpu.PowerMW)), VRAMUsed: reported(v&source.ValidMemUsed != 0, int64(gpu.MemUsed)),
			SMClockMHz: reported(v&source.ValidSMClock != 0, int64(gpu.SMClockMHz)), MemClockMHz: reported(v&source.ValidMemClock != 0, int64(gpu.MemClockMHz)),
			PowerLimitMW: reported(v&source.ValidPowerLimit != 0, int64(gpu.PowerLimitMW)), ThrottleOR: reported(v&source.ValidThrottleReason != 0, int64(gpu.ThrottleReason)),
			ECCErrors: reported(v&source.ValidECCUncorrected != 0, int64(gpu.ECCErrors)),
		}})
	}
}

func (l *Layer) gpuID(uuid string) int64 {
	for _, gpu := range l.devices {
		if gpu.device.UUID == uuid {
			return gpu.id
		}
	}
	return -1
}

func (l *Layer) registerProcess(pid uint32, seenAt int64) (processCache, bool) {
	start, command, err := l.readIdentity(uint(pid))
	if err != nil || start <= 0 {
		return processCache{}, false
	}
	key := procKey{Tgid: pid, StartTicks: start}
	identity := ProcessIdentity{BootID: l.bootID, TGID: int64(pid), StartTicks: start, Command: command, FirstSeenUnix: seenAt}
	l.addProcess(identity)
	pc := processCache{key: key, identity: identity}
	l.processCache[pid] = pc
	return pc, true
}

func (l *Layer) addProcess(p ProcessIdentity) {
	for _, old := range l.batch.Processes {
		if old.BootID == p.BootID && old.TGID == p.TGID && old.StartTicks == p.StartTicks {
			return
		}
	}
	l.batch.Processes = append(l.batch.Processes, p)
}

func (l *Layer) handleProcs(list source.ProcList) {
	l.batch.HasProcList = true
	l.batch.Sweep = list.Complete
	seen := make(map[procKey]struct{}, len(list.Procs))
	seenAt := list.TsNano / 1e9
	for _, proc := range list.Procs {
		pc, ok := l.processCache[uint32(proc.PID)]
		if !ok || pc.key.StartTicks == 0 {
			pc, ok = l.registerProcess(uint32(proc.PID), seenAt)
		} else {
			start, command, err := l.readIdentity(proc.PID)
			if err != nil {
				ok = false
			} else if start != pc.key.StartTicks {
				pc, ok = l.registerProcess(uint32(proc.PID), seenAt)
			} else {
				pc.identity.Command = command
				ok = true
			}
		}
		if !ok {
			continue
		}
		l.addProcess(pc.identity)
		seen[pc.key] = struct{}{}
		if !proc.VRAMValid {
			continue
		}
		gpuID := l.gpuID(proc.GPUUUID)
		if gpuID < 0 {
			continue
		}
		l.batch.ProcVRAM = append(l.batch.ProcVRAM, pendingVRAM{pc.key, ProcessVRAM{GPUID: gpuID, SeenAtUnix: seenAt, VRAMBytes: int64(proc.VRAMBytes)}})
	}
	l.batch.Seen = l.batch.Seen[:0]
	for key := range seen {
		l.batch.Seen = append(l.batch.Seen, key)
	}
}

func (l *Layer) resolveOrdinals(pc *processCache) map[uint32]int64 {
	if pc.resolved {
		return pc.ordinals
	}
	env, err := l.readEnviron(pc.key.Tgid)
	if err != nil {
		unattributed.Add(1)
		pc.resolved = true
		return nil
	}
	start, _, err := l.readIdentity(uint(pc.key.Tgid))
	if err != nil || start != pc.key.StartTicks {
		pc.resolved = true
		return nil
	}
	pc.ordinals = resolveCUDAOrdinals(env, l.devices)
	pc.resolved = true
	return pc.ordinals
}

func (l *Layer) handleAggregates(rows []source.AggRow) {
	groups := make(map[aggGroupKey]Aggregate)
	for _, row := range rows {
		pc, ok := l.processCache[row.Tgid]
		if !ok {
			unattributed.Add(1)
			continue
		}
		ordinals := l.resolveOrdinals(&pc)
		l.processCache[row.Tgid] = pc
		gpuID, ok := ordinals[row.Ordinal]
		if !ok {
			unattributed.Add(1)
			continue
		}
		minute := int64(uint64(row.TsNano) / uint64(time.Minute) * uint64(time.Minute) / uint64(time.Second))
		key := aggGroupKey{pc.key, gpuID, minute}
		a := groups[key]
		a.GPUID = gpuID
		foldAggregate(&a, row)
		groups[key] = a
	}
	for key, row := range groups {
		l.batch.Aggregates = append(l.batch.Aggregates, pendingAggregate{key: key.key, minute: key.minute, row: row})
	}
}

// foldAggregate maps each API's counters only to that API's columns.
func foldAggregate(a *Aggregate, row source.AggRow) {
	switch row.ApiID {
	case 4:
		a.Launches += int64(row.Count)
	case 7:
		a.MemcpyCalls += int64(row.Count)
		a.MemcpyBytes += int64(row.Bytes)
	case 5:
		a.AllocCalls += int64(row.Count)
		a.AllocBytes += int64(row.AllocBytes)
	case 6:
		a.FreeBytes += int64(row.FreeBytes)
	case 8:
		a.SyncCalls += int64(row.Count)
		a.SyncUsSum += int64(row.LatencySumNs / 1000)
		if m := int64(row.LatencyMaxNs / 1000); m > a.SyncUsMax {
			a.SyncUsMax = m
		}
	case 12:
		a.IoctlCalls += int64(row.Count)
	}
	a.Errors += int64(row.Errors)
	a.UvmFaults += int64(row.UvmFaults)
	a.UvmEvicts += int64(row.UvmEvicts)
}

func (l *Layer) handleEvent(event source.Event) {
	switch event.ApiID {
	case 16:
		l.registerProcess(event.Tgid, int64(event.TsNano/1e9))
	case 17:
		pc, ok := l.processCache[event.Tgid]
		if !ok || !startTicksMatchBoottime(event.StartBoottimeNs, pc.key.StartTicks) {
			return
		}
		var exitCode, termSignal *int
		status := int(event.Status)
		if status&0x7f != 0 {
			sig := status & 0x7f
			termSignal = &sig
		} else {
			code := (status >> 8) & 0xff
			exitCode = &code
		}
		l.queueProcessEnd(processEnd{key: pc.key, endTS: int64(event.TsNano / 1e9), reason: EndExit, exitCode: exitCode, termSig: termSignal})
		pc.exitQueued = true
		l.processCache[event.Tgid] = pc
	case 8:
		if event.Flags&source.FlagHungSync != 0 {
			l.batch.Incidents = append(l.batch.Incidents, *buildSyncHangIncident(event))
		}
		if event.LatencyNs >= 250_000_000 {
			l.batch.Incidents = append(l.batch.Incidents, *buildSyncStallIncident(event))
		}
	}
}

func (l *Layer) queueProcessEnd(end processEnd) {
	l.batch.ProcessEnds = append(l.batch.ProcessEnds, end)
}

func (l *Layer) handleXid(xid source.Xid) {
	incident := Incident{Type: IncidentXid, FirstTS: xid.TsNano / 1e9, LastTS: xid.TsNano / 1e9, DedupeKey: fmt.Sprintf("xid_%s_%d", xid.UUID, xid.Code), Summary: fmt.Sprintf("Xid %d on GPU %d", xid.Code, xid.Index), Detail: fmt.Sprintf("Xid error code %d", xid.Code)}
	if id := l.gpuID(xid.UUID); id >= 0 {
		incident.GPUID = &id
	}
	l.batch.Incidents = append(l.batch.Incidents, incident)
}

func (l *Layer) flush(ctx context.Context) error {
	if l.batch.rows() == 0 && !l.batch.Sweep {
		return nil
	}
	failed := l.batch
	l.batch = Batch{}
	writer := l.writeBatch
	if writer == nil {
		writer = l.WriteBatch
	}
	if err := writer(ctx, failed); err != nil {
		l.batch = mergeBatches(failed, l.batch)
		return err
	}
	for _, end := range failed.ProcessEnds {
		if pc, ok := l.processCache[end.key.Tgid]; ok && pc.key == end.key {
			delete(l.processCache, end.key.Tgid)
		}
	}
	return nil
}

func mergeBatches(older, newer Batch) Batch {
	out := Batch{}
	out.GPUMinutes = append(append(out.GPUMinutes, older.GPUMinutes...), newer.GPUMinutes...)
	out.Processes = append(append(out.Processes, older.Processes...), newer.Processes...)
	out.ProcVRAM = append(append(out.ProcVRAM, older.ProcVRAM...), newer.ProcVRAM...)
	out.Aggregates = append(append(out.Aggregates, older.Aggregates...), newer.Aggregates...)
	out.Incidents = append(append(out.Incidents, older.Incidents...), newer.Incidents...)
	out.ProcessEnds = append(append(out.ProcessEnds, older.ProcessEnds...), newer.ProcessEnds...)
	if newer.HasProcList {
		out.Seen = append(out.Seen, newer.Seen...)
		out.Sweep = newer.Sweep
		out.HasProcList = true
	} else {
		out.Seen = append(out.Seen, older.Seen...)
		out.Sweep = older.Sweep
		out.HasProcList = older.HasProcList
	}
	if out.rows() > maxRetryRows {
		drop := out.rows() - maxRetryRows
		log.Printf("Layer: retry queue full; dropping %d oldest row(s)", drop)
		trimBatch(&out, drop)
		source.Dropped.Add(uint64(drop))
	}
	return out
}

func trimBatch(b *Batch, drop int) {
	seenBefore := len(b.Seen)
	trim := func(n int) int {
		if drop >= n {
			drop -= n
			return n
		}
		nDrop := drop
		drop = 0
		return nDrop
	}
	for _, p := range []*[]timedGPU{&b.GPUMinutes} {
		n := trim(len(*p))
		*p = (*p)[n:]
	}
	for _, p := range []*[]ProcessIdentity{&b.Processes} {
		n := trim(len(*p))
		*p = (*p)[n:]
	}
	for _, p := range []*[]pendingVRAM{&b.ProcVRAM} {
		n := trim(len(*p))
		*p = (*p)[n:]
	}
	for _, p := range []*[]pendingAggregate{&b.Aggregates} {
		n := trim(len(*p))
		*p = (*p)[n:]
	}
	for _, p := range []*[]Incident{&b.Incidents} {
		n := trim(len(*p))
		*p = (*p)[n:]
	}
	for _, p := range []*[]processEnd{&b.ProcessEnds} {
		n := trim(len(*p))
		*p = (*p)[n:]
	}
	if drop > 0 {
		n := trim(len(b.Seen))
		b.Seen = b.Seen[n:]
	}
	if len(b.Seen) < seenBefore {
		b.Sweep = false
		b.HasProcList = false
	}
}

func (l *Layer) WriteBatch(ctx context.Context, batch Batch) error {
	if batch.rows() == 0 && !batch.Sweep {
		return nil
	}
	db := l.database
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return err
	}
	now := time.Now().UTC()
	metaTx, err := db.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer metaTx.Rollback()
	procIDs := make(map[procKey]int64, len(batch.Processes))
	for _, p := range batch.Processes {
		id, err := upsertProcessTx(ctx, metaTx, p)
		if err != nil {
			return err
		}
		procIDs[procKey{uint32(p.TGID), p.StartTicks}] = id
	}
	getID := func(key procKey) (int64, error) {
		if id, ok := procIDs[key]; ok {
			return id, nil
		}
		var id int64
		err := metaTx.QueryRowContext(ctx, `SELECT proc_id FROM procs WHERE boot_id=? AND tgid=? AND start_ticks=?`, l.bootID, key.Tgid, key.StartTicks).Scan(&id)
		return id, err
	}
	vram := make([]ProcessVRAM, 0, len(batch.ProcVRAM))
	for _, p := range batch.ProcVRAM {
		id, err := getID(p.key)
		if err != nil {
			return err
		}
		row := p.row
		row.ProcessID = id
		vram = append(vram, row)
	}
	if err := upsertProcessVRAMTx(ctx, metaTx, vram); err != nil {
		return err
	}
	for _, end := range batch.ProcessEnds {
		id, err := getID(end.key)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		end.procID = id
		if _, err := endProcessTx(ctx, metaTx, end); err != nil {
			return err
		}
	}
	for _, incident := range batch.Incidents {
		if _, err := db.writeIncidentTx(ctx, metaTx, incident); err != nil {
			return err
		}
	}
	if batch.Sweep {
		seen := make([]int64, 0, len(batch.Seen))
		for _, key := range batch.Seen {
			id, err := getID(key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			seen = append(seen, id)
		}
		if _, err := closeProcessesNotSeenTx(ctx, metaTx, l.bootID, seen, now.Unix()); err != nil {
			return err
		}
	}
	type dailyWrites struct {
		gpu map[int64][]GPUMinuteSample
		agg map[int64][]Aggregate
	}
	daily := make(map[string]*dailyWrites)
	getDaily := func(minute int64) *dailyWrites {
		date := time.Unix(minute, 0).UTC().Format(dayLayout)
		if daily[date] == nil {
			daily[date] = &dailyWrites{gpu: make(map[int64][]GPUMinuteSample), agg: make(map[int64][]Aggregate)}
		}
		return daily[date]
	}
	for _, timed := range batch.GPUMinutes {
		minute := timed.minute
		if minute == 0 {
			minute = now.Truncate(time.Minute).Unix()
		}
		getDaily(minute).gpu[minute] = append(getDaily(minute).gpu[minute], timed.sample)
	}
	for _, pending := range batch.Aggregates {
		id, err := getID(pending.key)
		if err != nil {
			return err
		}
		row := pending.row
		row.ProcessID = id
		getDaily(pending.minute).agg[pending.minute] = append(getDaily(pending.minute).agg[pending.minute], row)
	}
	dates := make([]string, 0, len(daily))
	for date := range daily {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	for _, date := range dates {
		if err := db.rotateDayDB(ctx, date); err != nil {
			return err
		}
		dayTx, err := db.day.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		writes := daily[date]
		for minute, rows := range writes.gpu {
			if err := writeGPUMinutesTx(ctx, dayTx, minute, rows); err != nil {
				dayTx.Rollback()
				return err
			}
		}
		for minute, rows := range writes.agg {
			if err := writeAggregatesTx(ctx, dayTx, minute, rows); err != nil {
				dayTx.Rollback()
				return err
			}
		}
		if err := dayTx.Commit(); err != nil {
			return err
		}
	}
	if err := metaTx.Commit(); err != nil {
		return err
	}
	return nil
}

func (l *Layer) Unattributed() uint64 { return unattributed.Load() }

func buildSyncStallIncident(event source.Event) *Incident {
	return &Incident{Type: IncidentSyncStall, FirstTS: int64(event.TsNano / 1e9), LastTS: int64(event.TsNano / 1e9), DedupeKey: fmt.Sprintf("sync_stall_%d_%d", event.Tgid, event.DeviceOrdinal), Summary: fmt.Sprintf("Sync stall: %dms", event.LatencyNs/1e6), Detail: fmt.Sprintf("Process %d sync took %d nanoseconds", event.Tgid, event.LatencyNs)}
}
func buildSyncHangIncident(event source.Event) *Incident {
	return &Incident{Type: IncidentSyncHang, FirstTS: int64(event.TsNano / 1e9), LastTS: int64(event.TsNano / 1e9), DedupeKey: fmt.Sprintf("sync_hang_%d_%d", event.Tgid, event.DeviceOrdinal), Summary: "Sync hang detected", Detail: fmt.Sprintf("Process %d sync hung for over 2 seconds", event.Tgid)}
}

func readProcessEnviron(pid uint32) ([]byte, error) {
	return os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/environ")
}

func reported(ok bool, value int64) *int64 {
	if !ok {
		return nil
	}
	return &value
}
