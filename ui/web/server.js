import express from 'express';
import { createServer } from 'http';
import { WebSocketServer } from 'ws';
import net from 'net';
import path from 'path';
import fs from 'fs';
import Database from 'better-sqlite3';
import { fileURLToPath } from 'url';
import { createServer as createViteServer } from 'vite';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// Use the project's daemon dev directory (relative to this file's location)
const baseDir = path.join(__dirname, '..', 'dev');
const socketPath = path.join(baseDir, 'run', 'wedjat.sock');
const dataDir = path.join(baseDir, 'var', 'lib', 'wedjat');

const app = express();
const server = createServer(app);

// Vite dev middleware — serves the React app with HMR, no build needed.
// It is mounted last: its SPA fallback answers every unmatched GET with
// index.html, so any route registered after it would never be reached.
const vite = await createViteServer({
  root: path.join(__dirname, 'frontend'),
  server: { middlewareMode: true, hmr: { server } },
  appType: 'spa',
});

// --- Unix socket bridge: persistent background connection ---

let daemonSocket = null;
let buffer = '';
const browserClients = new Set();
let lastDaemonGpuReceivedAt = 0;
let lastDaemonGpuTimestampMs = 0;
const DAEMON_GPU_GAP_MS = 1500;

// The daemon runs under sudo and creates the socket 0660 root:root, so an
// unprivileged server cannot reach it. Relax it once at startup; without this
// every connection attempt fails with EACCES and no live data ever arrives.
function tryFixSocketPerms() {
  try {
    fs.chmodSync(socketPath, 0o666);
    return true;
  } catch (e) {
    return false;
  }
}

// The daemon may be absent for a long stretch, so the retry loop reports a
// given failure once instead of on every 2s attempt.
let lastSocketError = '';

function reportSocketError(code, message) {
  if (lastSocketError === code) return;
  lastSocketError = code;

  if (code === 'EACCES' || code === 'EPERM') {
    console.error(`Daemon socket permission denied (${code}).`);
    if (tryFixSocketPerms()) {
      console.error('Relaxed socket permissions to 0666, retrying...');
      return;
    }
    console.error(
      `Could not chmod ${socketPath}. The daemon creates the socket as root, ` +
      'so this server must run under sudo too: sudo ./run.sh'
    );
  } else if (code === 'ENOENT') {
    console.error(
      'Daemon socket not found. Run "make dev" in /home/ebraam/wedjat/daemon.'
    );
  } else {
    console.error('Daemon socket error:', message);
  }
}

function connectToDaemon() {
  if (daemonSocket) return;

  const sock = net.connect(socketPath);
  daemonSocket = sock;

  sock.on('connect', () => {
    lastSocketError = '';
    console.log('Connected to daemon Unix socket');
  });

  sock.on('data', handleSocketData);

  sock.on('error', (err) => {
    sock.destroy();
    if (daemonSocket !== sock) return;
    daemonSocket = null;
    reportSocketError(err.code, err.message);
    setTimeout(connectToDaemon, 2000);
  });

  sock.on('close', () => {
    if (daemonSocket !== sock) return;
    daemonSocket = null;
    console.log('Daemon socket connection closed');
    broadcastToBrowsers(JSON.stringify({ type: 'daemon_disconnected' }));
    setTimeout(connectToDaemon, 2000);
  });
}

// Start connection loop immediately
connectToDaemon();

// Handle data from the daemon Unix socket
function handleSocketData(data) {
  buffer += data.toString();
  let newlineIdx;
  while ((newlineIdx = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, newlineIdx);
    buffer = buffer.slice(newlineIdx + 1);
    if (line.trim()) {
      logDaemonGpuGap(line);
      broadcastToBrowsers(line);
    }
  }
}

// This runs before browser fan-out. When it reports a gap, the browser UI is
// exonerated: the daemon socket itself did not deliver a contiguous GPU stream.
function logDaemonGpuGap(line) {
  let message;
  try {
    message = JSON.parse(line);
  } catch {
    return;
  }
  if (message.type !== 'gpu') return;

  const receivedAt = Date.now();
  const timestampMs = Number(message.timestamp_unix_nano) / 1e6;
  const receiveGapMs = lastDaemonGpuReceivedAt ? receivedAt - lastDaemonGpuReceivedAt : null;
  const sourceGapMs = lastDaemonGpuTimestampMs && Number.isFinite(timestampMs)
    ? timestampMs - lastDaemonGpuTimestampMs
    : null;

  if ((receiveGapMs != null && receiveGapMs >= DAEMON_GPU_GAP_MS) ||
      (sourceGapMs != null && (sourceGapMs >= DAEMON_GPU_GAP_MS || sourceGapMs <= 0))) {
    console.warn('[Wedjat bridge] GPU gap from daemon socket', {
      receiveGapMs,
      daemonSampleGapMs: sourceGapMs,
      daemonSampleAgeMs: Number.isFinite(timestampMs) ? receivedAt - timestampMs : null,
      daemonTimestampMs: Number.isFinite(timestampMs) ? timestampMs : null,
      rows: Array.isArray(message.data) ? message.data.length : 0,
    });
  }

  lastDaemonGpuReceivedAt = receivedAt;
  if (Number.isFinite(timestampMs) && timestampMs > 0) lastDaemonGpuTimestampMs = timestampMs;
}

// Broadcast a line to all connected browser WebSockets
function broadcastToBrowsers(message) {
  browserClients.forEach(ws => {
    if (ws.readyState === ws.OPEN) {
      try {
        ws.send(message);
      } catch (e) {
        browserClients.delete(ws);
      }
    }
  });
}

// Check if the daemon socket is currently connected. A pending socket is not
// connected yet, so readyState is what decides, not the null check alone.
function isDaemonConnected() {
  return daemonSocket !== null && !daemonSocket.connecting;
}

// --- API endpoints ---

// Open a database read-only. The daemon owns these files and runs as root, so
// a read-write open can fail on permissions or leave stray -wal files behind.
function openDb(file) {
  return new Database(path.join(dataDir, file), { readonly: true, fileMustExist: true });
}

app.get('/api/status', async (req, res) => {
  const connected = isDaemonConnected();
  let heartbeatAge = -1;
  let warning = '';

  try {
    const db = openDb('meta.db');
    const row = db.prepare(
      "SELECT v FROM daemon_state WHERE k = 'heartbeat_ts'"
    ).get();
    db.close();
    if (row) {
      heartbeatAge = Math.floor((Date.now() / 1000) - parseInt(row.v, 10));
    }
  } catch (e) {}

  if (!connected) {
    warning = 'Dev daemon not connected. Run "make dev" in /home/ebraam/wedjat/daemon to start it.';
  } else if (heartbeatAge > 30) {
    warning = `Dev daemon heartbeat stale (${heartbeatAge}s ago).`;
  }

  res.json({ connected, heartbeat_age: heartbeatAge, warning, socket_path: socketPath });
});

app.get('/api/gpus', (req, res) => {
  try {
    const db = openDb('meta.db');
    const rows = db.prepare(
      `SELECT gpu_id, uuid, idx, name, pci_bus_id, vram_total_bytes, driver_version, first_seen_ts, last_seen_ts FROM gpus ORDER BY idx`
    ).all();
    db.close();
    res.json(rows.map(g => ({
      id: g.gpu_id, uuid: g.uuid, index: g.idx, name: g.name,
      pci_bus_id: g.pci_bus_id, vram_total_bytes: g.vram_total_bytes,
      driver_version: g.driver_version,
      first_seen: new Date(g.first_seen_ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
      last_seen: new Date(g.last_seen_ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
    })));
  } catch (e) {
    res.status(500).json({ error: e.message });
  }
});

app.get('/api/processes', (req, res) => {
  try {
    const db = openDb('meta.db');
    const rows = db.prepare(
      `SELECT p.proc_id, p.tgid, p.command, g.name as gpu_name, g.uuid as gpu_uuid,
              pg.last_vram_bytes, pg.peak_vram_bytes, p.first_seen_ts, p.end_ts
       FROM procs p
       JOIN proc_gpu pg ON p.proc_id = pg.proc_id
       JOIN gpus g ON pg.gpu_id = g.gpu_id
       WHERE p.end_ts IS NULL
       ORDER BY pg.last_vram_bytes DESC`
    ).all();
    db.close();
    res.json(rows.map(p => ({
      id: p.proc_id, pid: p.tgid, command: p.command, gpu: p.gpu_name, gpu_uuid: p.gpu_uuid,
      last_vram_bytes: p.last_vram_bytes, peak_vram_bytes: p.peak_vram_bytes,
      first_seen: new Date(p.first_seen_ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
      running: true,
    })));
  } catch (e) {
    res.status(500).json({ error: e.message });
  }
});

// todayDb opens the current UTC daily database with meta.db attached, which
// every daily endpoint needs for its GPU and process name lookups.
function todayDb() {
  const today = new Date().toISOString().slice(0, 10);
  const db = openDb(today + '.db');
  db.exec(`ATTACH DATABASE '${path.join(dataDir, 'meta.db')}' AS meta`);
  return db;
}

app.get('/api/history', (req, res) => {
  const limit = parseInt(req.query.limit) || 100;

  try {
    const db = todayDb();
    const rows = db.prepare(
      `SELECT gs.ts, m.name, m.uuid, gs.n,
              gs.util_gpu_sum as util_sum,
              gs.util_gpu_max as util_max,
              gs.temp_max as temp_max,
              gs.vram_used_max as vram_max,
              gs.power_mw_sum as power_sum
       FROM gpu_samples gs
       JOIN meta.gpus m ON gs.gpu_id = m.gpu_id
       ORDER BY gs.ts DESC
       LIMIT ?`
    ).all(limit);
    db.close();
    res.json(rows.map(s => ({
      ts: s.ts,
      time: new Date(s.ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
      gpu_name: s.name, gpu_uuid: s.uuid, n: s.n,
      util_gpu_avg: s.n > 0 && s.util_sum != null ? Math.floor(s.util_sum / s.n) : null,
      util_gpu_max: s.util_max, temp_max_c: s.temp_max,
      vram_used_max_bytes: s.vram_max, power_mw_sum: s.power_sum,
    })));
  } catch (e) {
    res.status(503).json({ error: 'Cannot open history DB: ' + e.message });
  }
});

app.get('/api/aggregates', (req, res) => {
  const limit = parseInt(req.query.limit) || 100;

  try {
    const db = todayDb();
    const rows = db.prepare(
      `SELECT a.ts, p.command, p.tgid, g.name as gpu_name,
              a.launches, a.memcpy_calls, a.memcpy_bytes, a.alloc_calls, a.alloc_bytes, a.free_bytes, a.sync_calls, a.sync_us_sum, a.sync_us_max, a.ioctl_calls, a.uvm_faults, a.uvm_evicts, a.errors
       FROM agg a
       JOIN meta.procs p ON a.proc_id = p.proc_id
       JOIN meta.gpus g ON a.gpu_id = g.gpu_id
       ORDER BY a.ts DESC
       LIMIT ?`
    ).all(limit);
    db.close();
    res.json(rows.map(a => ({
      ...a,
      time: new Date(a.ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
    })));
  } catch (e) {
    res.status(503).json({ error: 'Cannot open DB: ' + e.message });
  }
});

app.get('/api/incidents', (req, res) => {
  const limit = parseInt(req.query.limit) || 100;

  try {
    const db = openDb('meta.db');
    const rows = db.prepare(
      `SELECT i.incident_id, i.type, i.first_ts, i.last_ts, i.occurrences, i.summary, i.detail,
              p.command, p.tgid, g.name as gpu_name
       FROM incidents i
       LEFT JOIN procs p ON i.proc_id = p.proc_id
       LEFT JOIN gpus g ON i.gpu_id = g.gpu_id
       ORDER BY i.last_ts DESC
       LIMIT ?`
    ).all(limit);
    db.close();
    res.json(rows.map(i => ({
      ...i,
      first_time: new Date(i.first_ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
      last_time: new Date(i.last_ts * 1000).toISOString().replace('T', ' ').substring(0, 19),
    })));
  } catch (e) {
    res.status(503).json({ error: 'Cannot open DB: ' + e.message });
  }
});

// --- WebSocket: broadcast daemon snapshots to browsers ---

const wss = new WebSocketServer({ noServer: true });

wss.on('connection', (ws) => {
  console.log('Browser connected, adding to broadcast set');
  browserClients.add(ws);

  ws.on('close', () => {
    browserClients.delete(ws);
  });

  ws.on('pong', () => {
    ws.isAlive = true;
  });
});

server.on('upgrade', (request, socket, head) => {
  const pathname = new URL(request.url, 'http://localhost').pathname;
  if (pathname === '/socket') {
    wss.handleUpgrade(request, socket, head, (ws) => {
      wss.emit('connection', ws, request);
    });
    return;
  }
  // Vite registers its own upgrade listener for the HMR socket, which uses
  // the `vite-hmr` subprotocol on "/". Destroying it here would silently kill
  // hot reload, so leave any other upgrade to the listener that owns it.
  if (request.headers['sec-websocket-protocol'] === 'vite-hmr') return;
  socket.destroy();
});

// Mounted after every API route: the SPA fallback answers all unmatched GETs
// with index.html and would shadow any route registered below it.
app.use(vite.middlewares);

const PORT = process.env.PORT || 3000;
server.listen(PORT, () => {
  console.log(`Wedjat Dev UI server running on http://localhost:${PORT}`);
  console.log('Socket path:', socketPath);
  console.log('Data dir:', dataDir);
});
