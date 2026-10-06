// Shared fetch helper for the API endpoints.
//
// Every endpoint here can answer with a non-2xx status carrying an { error }
// body, so a bare `r.json()` would hand that object to the caller as if it
// were data and the page would crash on .map. This rejects instead.

export async function apiGet(path) {
  const res = await fetch(path);

  let body;
  try {
    body = await res.json();
  } catch {
    throw new Error(`${res.status} ${res.statusText || 'request failed'}`);
  }

  if (!res.ok) {
    throw new Error(body?.error || `${res.status} ${res.statusText || 'request failed'}`);
  }

  return body;
}

export function formatBytes(bytes) {
  if (bytes == null) return '-';
  if (bytes === 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1048576) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1073741824) return `${(bytes / 1048576).toFixed(1)} MB`;
  return `${(bytes / 1073741824).toFixed(2)} GB`;
}