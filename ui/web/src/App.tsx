import { useEffect, useMemo, useState } from 'react';
import { Link, Route, Routes, useLocation } from 'react-router-dom';
import { apiGet } from './api';
import { TelemetryStore } from './store/telemetryStore';
import { useStoreValue } from './hooks/useStoreValue';
import { useTheme } from './hooks/useTheme';
import { DEBUG } from './flags';
import StatusBar from './components/StatusBar';
import DebugOverlay from './components/DebugOverlay';
import AsciiArt from './components/AsciiArt';
import Dashboard from './pages/Dashboard';
import Processes from './pages/Processes';
import Events from './pages/Events';
import History from './pages/History';
import GpuDetail from './pages/GpuDetail';
import GPUs from './pages/GPUs';
import Incidents from './pages/Incidents';
import maskArt from '../../../assets/ascii/mask.txt?raw';
import type { StatusResponse, GpuInfo } from './types';

export default function App() {
  const store = useMemo(() => new TelemetryStore(), []);
  const location = useLocation();
  const { theme, toggleTheme } = useTheme();
  const connected = useStoreValue(store, 'connection', (state) => state.socketConnected, 0);
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [statusError, setStatusError] = useState('');
  const [gpus, setGpus] = useState<GpuInfo[]>([]);
  const [selectedGpu, setSelectedGpu] = useState<string>('all');
  const [live, setLive] = useState(true);

  useEffect(() => {
    store.connect();
  }, [store]);

  useEffect(() => {
    let active = true;
    const refreshStatus = () =>
      apiGet<StatusResponse>('/api/status')
        .then((value) => {
          if (active) {
            setStatus(value);
            setStatusError('');
          }
        })
        .catch((error) => {
          if (active) setStatusError(`Cannot reach API: ${error.message}`);
        });
    refreshStatus();
    const timer = setInterval(refreshStatus, 5000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, []);

  useEffect(() => {
    let active = true;
    apiGet<GpuInfo[]>('/api/gpus')
      .then((rows) => {
        if (active) setGpus(rows);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);

  const path = location.pathname;
  const linkClass = (href: string) =>
    `nav-link${path === href ? ' nav-link-active' : ''}`;
  const filteredGpu = selectedGpu === 'all' ? null : selectedGpu;
  const disconnected =
    !connected ||
    status?.connected === false ||
    (Number.isFinite(status?.heartbeat_age) && (status?.heartbeat_age ?? 0) > 30);
  const breadcrumb = path === '/' ? '' : path;

  return (
    <div className={`app-shell${disconnected ? ' is-stale' : ''}`}>
      <StatusBar
        store={store}
        connected={connected}
        status={status}
        statusError={statusError}
        gpus={gpus}
        selectedGpu={selectedGpu}
        onSelectGpu={setSelectedGpu}
        live={live}
        onToggleLive={() => setLive((v) => !v)}
        theme={theme}
        onToggleTheme={toggleTheme}
      />

      {/* Shell body: sidebar + content area */}
      <div className="flex min-h-[calc(100vh-var(--topbar-height)-50px)]">
        {/* Sidebar nav */}
        <nav
          className="sidebar-nav flex flex-col gap-[2px] p-4 min-w-[160px] border-r border-dashed border-border"
          aria-label="Main navigation"
        >
          <Link className={linkClass('/')} to="/">
            Live
          </Link>
          <Link className={linkClass('/processes')} to="/processes">
            Processes
          </Link>
          <Link className={linkClass('/events')} to="/events">
            Events
          </Link>
          <Link className={linkClass('/incidents')} to="/incidents">
            Incidents
          </Link>
           <Link className={linkClass('/history')} to="/history">
             History
           </Link>
           <Link className={linkClass('/gpus')} to="/gpus">
             GPUs
           </Link>
        </nav>

        {/* Content area */}
        <div className="flex-1 min-w-0">
          {breadcrumb && (
            <div className="px-[var(--page-padding-x)] py-2 text-text-dim text-xs font-mono tracking-wide">
              {breadcrumb}
            </div>
          )}
          <main className="page-wrap">
            <Routes>
              <Route
                path="/"
                element={
                  <Dashboard
                    store={store}
                    gpus={gpus}
                    selectedGpu={filteredGpu}
                    disconnected={disconnected}
                    live={live}
                    onToggleLive={() => setLive((v) => !v)}
                  />
                }
              />
              <Route
                path="/gpu/:id"
                element={<GpuDetail store={store} gpus={gpus} />}
              />
              <Route
                path="/processes"
                element={
                  <Processes
                    store={store}
                    gpus={gpus}
                    selectedGpu={filteredGpu}
                    disconnected={disconnected}
                  />
                }
              />
              <Route
                path="/events"
                element={
                  <Events
                    store={store}
                    gpus={gpus}
                    selectedGpu={filteredGpu}
                    disconnected={disconnected}
                  />
                }
              />
              <Route
                path="/incidents"
                element={<Incidents />}
              />
              <Route
                path="/history"
                element={<History gpus={gpus} selectedGpu={filteredGpu} />}
              />
              <Route
                path="/gpus"
                element={<GPUs store={store} gpus={gpus} />}
              />
              <Route
                path="*"
                element={
                  <section className="glass-panel empty-state rounded-xl p-6">
                    <AsciiArt art={maskArt} />
                    <h2>Page not found</h2>
                    <Link to="/" className="text-chart-line hover:underline">
                      Return to live monitor
                    </Link>
                  </section>
                }
              />
            </Routes>
          </main>
        </div>
      </div>

      {/* Footer */}
      <footer className="footer">
        WEDJAT &middot; Single host GPU telemetry &middot; Data can be incomplete when socket messages are dropped
      </footer>

      {DEBUG && <DebugOverlay store={store} />}
    </div>
  );
}
