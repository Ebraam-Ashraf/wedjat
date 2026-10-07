// Dev/diagnostic flags, read once from the query string.
//
// `?debug=1` is used rather than import.meta.env.DEV so the overlay works in any
// build, which is what makes it useful for checking a running server.
//
//   ?synthetic=1  feed the store fake varying data at 2Hz, no socket
//   ?debug=1      show the interval / rate / fps overlay

const params = typeof window !== 'undefined' && window.location
  ? new URLSearchParams(window.location.search)
  : new URLSearchParams();

export const DEBUG: boolean = params.get('debug') === '1';
export const SYNTHETIC: boolean = params.get('synthetic') === '1';
