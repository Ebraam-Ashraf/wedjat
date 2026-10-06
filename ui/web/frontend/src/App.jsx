import React, { useEffect, useMemo, useState } from 'react';
import { Link, Route, Routes, useLocation } from 'react-router-dom';
import { apiGet } from './api.js';
import { TelemetryStore } from './store/telemetryStore.js';
import { useStoreValue } from './hooks/useStoreValue.js';
import { DEBUG } from './flags.js';
import StatusBar from './components/StatusBar.jsx';
import DebugOverlay from './components/DebugOverlay.jsx';
import Live from './pages/Dashboard.jsx';
import Processes from './pages/Processes.jsx';
import Events from './pages/Events.jsx';
import History from './pages/History.jsx';
import GpuDetail from './pages/GpuDetail.jsx';

export default function App() {
  const store = useMemo(() => new TelemetryStore(), []);
  const location = useLocation();
  const connected = useStoreValue(store, 'connection', (state) => state.socketConnected, 0);
  const [status, setStatus] = useState(null);
  const [statusError, setStatusError] = useState('');
  const [gpus, setGpus] = useState([]);
  const [selectedGpu, setSelectedGpu] = useState('all');
  const [live, setLive] = useState(true);

  useEffect(() => store.connect(), [store]);

  useEffect(() => {
    let active = true;
    const refreshStatus = () => apiGet('/api/status')
      .then((value) => { if (active) { setStatus(value); setStatusError(''); } })
      .catch((error) => { if (active) setStatusError(`Cannot reach API: ${error.message}`); });
    refreshStatus();
    const timer = setInterval(refreshStatus, 5000);
    return () => { active = false; clearInterval(timer); };
  }, []);

  useEffect(() => {
    let active = true;
    apiGet('/api/gpus').then((rows) => { if (active) setGpus(rows); }).catch(() => {});
    return () => { active = false; };
  }, []);

  const path = location.pathname;
  const linkClass = (href) => `nav-link ${path === href ? 'nav-link-active' : ''}`;
  const filteredGpu = selectedGpu === 'all' ? null : selectedGpu;
  const disconnected = !connected || status?.connected === false || (Number.isFinite(status?.heartbeat_age) && status.heartbeat_age > 30);

  return <div className={`app-shell ${disconnected ? 'is-stale' : ''}`}>
    <StatusBar store={store} connected={connected} status={status} statusError={statusError} gpus={gpus} selectedGpu={selectedGpu} onSelectGpu={setSelectedGpu} live={live} onToggleLive={() => setLive((value) => !value)} />
    <div className="subnav">
      <Link className={linkClass('/')} to="/">Live</Link>
      <Link className={linkClass('/processes')} to="/processes">Processes</Link>
      <Link className={linkClass('/events')} to="/events">Events</Link>
      <Link className={linkClass('/history')} to="/history">History</Link>
    </div>
    <main className="page-wrap">
      <Routes>
        <Route path="/" element={<Live store={store} gpus={gpus} selectedGpu={filteredGpu} disconnected={disconnected} live={live} onToggleLive={() => setLive((value) => !value)} />} />
        <Route path="/gpu/:id" element={<GpuDetail store={store} gpus={gpus} />} />
        <Route path="/processes" element={<Processes store={store} gpus={gpus} selectedGpu={filteredGpu} disconnected={disconnected} />} />
        <Route path="/events" element={<Events store={store} gpus={gpus} selectedGpu={filteredGpu} disconnected={disconnected} />} />
        <Route path="/history" element={<History gpus={gpus} selectedGpu={filteredGpu} />} />
        <Route path="*" element={<section className="panel empty-state"><h1>Page not found</h1><Link to="/">Return to live monitor</Link></section>} />
      </Routes>
    </main>
    <footer className="footer">WEDJAT · Single host GPU telemetry · Data can be incomplete when socket messages are dropped</footer>
    {DEBUG && <DebugOverlay store={store} />}
  </div>;
}
