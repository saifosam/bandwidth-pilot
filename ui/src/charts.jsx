import { fmtRate, fmtBytes } from './format.js';

// Rounds a maximum up to a tidy axis value.
function niceMax(v, floor) {
  v = Math.max(v, floor);
  const p = Math.pow(2, Math.floor(Math.log2(v)));
  for (const m of [1, 1.5, 2, 3, 4]) if (p * m >= v) return p * m;
  return p * 4;
}

export function Sparkline({ data, width = 92, height = 22 }) {
  if (!data || data.length < 2) return <svg class="spark" width={width} height={height} />;
  const max = Math.max(1024, ...data);
  const step = width / 59;
  const x0 = width - (data.length - 1) * step;
  const pts = data.map((v, i) => `${(x0 + i * step).toFixed(1)},${(height - 1.5 - (v / max) * (height - 3)).toFixed(1)}`).join(' ');
  return (
    <svg class="spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
      <polyline points={pts} fill="none" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" />
    </svg>
  );
}

// Live download/upload chart for the last minute.
export function RateChart({ hist, units }) {
  const W = 356, H = 132, L = 0, R = 0, T = 8, B = 18;
  const dl = hist ? hist.in : [], ul = hist ? hist.out : [];
  const max = niceMax(Math.max(0, ...dl, ...ul), 8 * 1024);
  const step = (W - L - R) / 59;
  const y = (v) => T + (1 - v / max) * (H - T - B);
  const line = (d) => {
    const x0 = W - R - (d.length - 1) * step;
    return d.map((v, i) => `${(x0 + i * step).toFixed(1)},${y(v).toFixed(1)}`);
  };
  const dlPts = line(dl), ulPts = line(ul);
  const area = dlPts.length > 1
    ? `M${dlPts[0].split(',')[0]},${y(0)} L${dlPts.join(' L')} L${W - R},${y(0)} Z` : '';
  return (
    <svg class="ratechart" viewBox={`0 0 ${W} ${H}`} role="img"
      aria-label={`Download ${fmtRate(dl[dl.length - 1] || 0, units)}, upload ${fmtRate(ul[ul.length - 1] || 0, units)}`}>
      {[0, 0.5, 1].map((f) => (
        <line x1={L} x2={W - R} y1={y(max * f)} y2={y(max * f)} class="grid" />
      ))}
      <text x={L + 2} y={y(max) + 11} class="axis">{fmtRate(max, units)}</text>
      <text x={L + 2} y={y(max / 2) + 11} class="axis">{fmtRate(max / 2, units)}</text>
      <text x={L + 2} y={H - 4} class="axis">60 s ago</text>
      <text x={W - R - 2} y={H - 4} class="axis" text-anchor="end">now</text>
      {area && <path d={area} class="dl-area" />}
      {dlPts.length > 1 && <polyline points={dlPts.join(' ')} class="dl-line" />}
      {ulPts.length > 1 && <polyline points={ulPts.join(' ')} class="ul-line" />}
    </svg>
  );
}

// Daily totals for the last 30 days: download and upload side by side.
export function UsageBars({ days }) {
  const W = 880, H = 190, L = 56, R = 8, T = 10, B = 24;
  const max = niceMax(Math.max(0, ...days.map((d) => Math.max(d.in, d.out))), 1024 * 1024);
  const slot = (W - L - R) / days.length;
  const bw = Math.max(2, slot * 0.34);
  const h = (v) => (v / max) * (H - T - B);
  const label = (date) => {
    const [, m, d] = date.split('-');
    return `${+d}/${+m}`;
  };
  return (
    <svg class="usagebars" viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Data used per day over the last 30 days">
      {[0, 0.5, 1].map((f) => (
        <g>
          <line x1={L} x2={W - R} y1={H - B - h(max * f)} y2={H - B - h(max * f)} class="grid" />
          <text x={L - 8} y={H - B - h(max * f) + 4} class="axis" text-anchor="end">{fmtBytes(max * f)}</text>
        </g>
      ))}
      {days.map((d, i) => {
        const x = L + i * slot + slot / 2;
        return (
          <g>
            <title>{`${d.date}\nDownload ${fmtBytes(d.in)}\nUpload ${fmtBytes(d.out)}`}</title>
            <rect x={L + i * slot} y={T} width={slot} height={H - T - B} fill="transparent" />
            <rect x={x - bw - 0.5} y={H - B - h(d.in)} width={bw} height={h(d.in)} class="dl-bar" rx="1" />
            <rect x={x + 0.5} y={H - B - h(d.out)} width={bw} height={h(d.out)} class="ul-bar" rx="1" />
            {(i % 5 === 4 || i === days.length - 1) && (
              <text x={x} y={H - 7} class="axis" text-anchor="middle">{i === days.length - 1 ? 'Today' : label(d.date)}</text>
            )}
          </g>
        );
      })}
    </svg>
  );
}
