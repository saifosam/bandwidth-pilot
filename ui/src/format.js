export const KB = 1024;
export const MB = 1024 * KB;
export const GB = 1024 * MB;

function trim(n, digits) {
  return n.toFixed(digits);
}

// Formats a speed given in bytes per second.
export function fmtRate(bps, units = 'auto') {
  bps = Math.max(0, bps || 0);
  if (units === 'bits') {
    const b = bps * 8;
    if (b >= 1e6) return trim(b / 1e6, 2) + ' Mbit/s';
    if (b >= 1e3) return trim(b / 1e3, 1) + ' kbit/s';
    return Math.round(b) + ' bit/s';
  }
  if (units === 'kb') return trim(bps / KB, bps < 100 * KB ? 2 : 1) + ' KB/s';
  if (units === 'mb') return trim(bps / MB, 2) + ' MB/s';
  if (bps >= MB) return trim(bps / MB, 2) + ' MB/s';
  if (bps >= KB) return trim(bps / KB, bps >= 100 * KB ? 0 : 1) + ' KB/s';
  return Math.round(bps) + ' B/s';
}

// Formats an amount of data given in bytes.
export function fmtBytes(n) {
  n = Math.max(0, n || 0);
  if (n >= GB) return trim(n / GB, 2) + ' GB';
  if (n >= MB) return trim(n / MB, n >= 100 * MB ? 0 : 1) + ' MB';
  if (n >= KB) return trim(n / KB, n >= 100 * KB ? 0 : 1) + ' KB';
  return Math.round(n) + ' B';
}

export const PRIORITIES = ['Low', 'Normal', 'High', 'Critical'];

// A stable colour for an app's letter tile.
export function hueOf(text) {
  let h = 0;
  for (let i = 0; i < text.length; i++) h = (h * 31 + text.charCodeAt(i)) >>> 0;
  return h % 360;
}
