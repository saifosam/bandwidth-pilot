const PATHS = {
  down: 'M8 2.5v10.5M3.8 9l4.2 4.2L12.2 9',
  up: 'M8 13.5V3M3.8 7L8 2.8 12.2 7',
  chevron: 'M6 3.5L10.5 8 6 12.5',
  search: 'M7 11.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9zM10.4 10.4L14 14',
  block: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM3.8 3.8l8.4 8.4',
  gauge: 'M2.6 12a5.9 5.9 0 1 1 10.8 0M8 10.2l2.6-3.6',
  flag: 'M4 14.2V2.4M4 3h8l-1.8 2.6L12 8.2H4',
  quota: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM8 8V2M8 8l4.4 3.9',
  close: 'M3.8 3.8l8.4 8.4M12.2 3.8l-8.4 8.4',
  folder: 'M1.8 4h4.4l1.4 1.6h6.6v7H1.8z',
  monitor: 'M2 3h12v8.2H2zM5.8 13.6h4.4M8 11.2v2.4',
  trash: 'M2.8 4.4h10.4M6.4 4.4V2.8h3.2v1.6M4.2 4.4l.6 9h6.4l.6-9',
  info: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM8 7.2v4M8 4.9v.2',
  check: 'M3.2 8.4l3.2 3.2 6.4-7',
  clock: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM8 4.6V8l2.4 1.6',
  globe: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM2 8h12M8 2c1.8 1.8 2.6 3.8 2.6 6S9.8 12.2 8 14M8 2C6.2 3.8 5.4 5.8 5.4 8S6.2 12.2 8 14',
  list: 'M5.5 4h8.5M5.5 8h8.5M5.5 12h8.5M2.2 4h.3M2.2 8h.3M2.2 12h.3',
  shield: 'M8 14s5-2.2 5-6.4V3.6L8 2 3 3.6v4c0 4.2 5 6.4 5 6.4z',
  split: 'M3 3h4v4H3zM9 9h4v4H9zM5 7v4h4',
  plus: 'M8 3v10M3 8h10',
};

export function Icon({ name, size = 16, class: cls = '' }) {
  return (
    <svg class={'icon ' + cls} width={size} height={size} viewBox="0 0 16 16" fill="none"
      stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d={PATHS[name]} />
    </svg>
  );
}

// The app logo (simplified for small sizes).
export function Logo() {
  return (
<svg width="24" height="24" viewBox="0 0 256 256" aria-hidden="true">
  <defs>
    <linearGradient id="bp-bg" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#1d3b6e"/>
      <stop offset="1" stop-color="#0a1428"/>
    </linearGradient>
    <linearGradient id="bp-arc" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0" stop-color="#38e1a6"/>
      <stop offset="0.55" stop-color="#4cc9ff"/>
      <stop offset="1" stop-color="#a77bff"/>
    </linearGradient>
    <linearGradient id="bp-needle" x1="0" y1="1" x2="1" y2="0">
      <stop offset="0" stop-color="#ffb347"/>
      <stop offset="1" stop-color="#ff5e62"/>
    </linearGradient>
    <radialGradient id="bp-glow" cx="0.5" cy="0.62" r="0.5">
      <stop offset="0" stop-color="#4cc9ff" stop-opacity="0.35"/>
      <stop offset="1" stop-color="#4cc9ff" stop-opacity="0"/>
    </radialGradient>
    <clipPath id="bp-tile"><rect x="8" y="8" width="240" height="240" rx="56"/></clipPath>
    <filter id="bp-soft" x="-30%" y="-30%" width="160%" height="160%">
      <feGaussianBlur stdDeviation="5"/>
    </filter>
  </defs>
  <rect x="0" y="0" width="256" height="256" rx="52" fill="url(#bp-bg)"/>
  <rect x="8.5" y="8.5" width="239" height="239" rx="55.5" fill="none" stroke="#ffffff" stroke-opacity="0.12"/>
  <circle cx="128" cy="150" r="110" fill="url(#bp-glow)" clip-path="url(#bp-tile)"/>
  {/* gauge arc: 210deg sweep around (128,150), r=78 */}
  <path d="M 60.45 189 A 78 78 0 1 1 195.55 189" fill="none" stroke="url(#bp-arc)" stroke-width="20" stroke-linecap="round" opacity="0"/>
  <path d="M 60.45 189 A 78 78 0 1 1 195.55 189" fill="none" stroke="url(#bp-arc)" stroke-width="30" stroke-linecap="round"/>
  {/* ticks */}
  
  {/* needle pointing up-right */}
  <path d="M 114 154 L 192 90 L 142 172 Z" fill="url(#bp-needle)" opacity="0"/>
  <path d="M 114 154 L 192 90 L 142 172 Z" fill="url(#bp-needle)"/>
  <circle cx="128" cy="160" r="24" fill="#ffffff"/>
  <circle cx="128" cy="160" r="9" fill="#0a1428"/>
</svg>
  );
}
