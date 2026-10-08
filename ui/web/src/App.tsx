import { useEffect, useMemo, useState } from 'react';
import { Link, Route, Routes, useLocation } from 'react-router-dom';
import { apiGet } from './api';
import { TelemetryStore } from './store/telemetryStore';
import { useStoreValue } from './hooks/useStoreValue';
import { useTheme } from './hooks/useTheme';
import { DEBUG } from './flags';
import StatusBar from './components/StatusBar';
import TerminalShell from './components/TerminalShell';
import DebugOverlay from './components/DebugOverlay';
import AsciiArt from './components/AsciiArt';
import Dashboard from './pages/Dashboard';
import Processes from './pages/Processes';
import History from './pages/History';
import GpuDetail from './pages/GpuDetail';
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
  const filteredGpu = selectedGpu === 'all' ? null : selectedGpu;
  const disconnected =
    !connected ||
    status?.connected === false ||
    (Number.isFinite(status?.heartbeat_age) && (status?.heartbeat_age ?? 0) > 30);
  const breadcrumb = path === '/' ? '' : path;

  return (
    <div className={`app-shell${disconnected ? ' is-stale' : ''}`}>
      <TerminalShell
        header={
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
        }
        connected={connected}
        theme={theme}
        onToggleTheme={toggleTheme}
        gpuCount={gpus.length}
      >
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
                path="/incidents"
                element={<Incidents />}
              />
              <Route
                path="/history"
                element={<History gpus={gpus} selectedGpu={filteredGpu} />}
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
      </TerminalShell>

      {DEBUG && <DebugOverlay store={store} />}
    </div>
  );
}
