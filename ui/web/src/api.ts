// Shared fetch helper for the API endpoints.
//
// Every endpoint here can answer with a non-2xx status carrying an { error }
// body, so a bare `r.json()` would hand that object to the caller as if it
// were data and the page would crash on .map. This rejects instead.

export async function apiGet<T = unknown>(path: string): Promise<T> {
  const res = await fetch(path);

  let body: unknown;
  try {
    body = await res.json();
  } catch {
    throw new Error(`${res.status} ${res.statusText || 'request failed'}`);
  }

  if (!res.ok) {
    throw new Error((body as { error?: string })?.error || `${res.status} ${res.statusText || 'request failed'}`);
  }

  return body as T;
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null) return '-';
  if (bytes === 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1_048_576) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1_073_741_824) return `${(bytes / 1_048_576).toFixed(1)} MB`;
  return `${(bytes / 1_073_741_824).toFixed(2)} GB`;
}
