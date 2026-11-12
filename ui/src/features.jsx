import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { call } from './api.js';
import { fmtRate, fmtBytes, hueOf, KB } from './format.js';
import { Icon } from './icons.jsx';
import { Switch, RateField } from './controls.jsx';

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

export const DAY_LETTERS = ['S', 'M', 'T', 'W', 'T', 'F', 'S'];
const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const WEEKDAYS = [false, true, true, true, true, true, false];
const DEFAULT_SCHEDULE = { on: true, days: [true, true, true, true, true, true, true], from: 18 * 60, to: 23 * 60 };

export const fmtTime = (m) => `${String(Math.floor(m / 60) % 24).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
const parseTime = (s) => {
  const [h, m] = String(s).split(':').map(Number);
  return isFinite(h) && isFinite(m) ? h * 60 + m : 0;
};

// "Mon-Fri", "Every day", "Sat, Sun"...
export function daysText(days) {
  const on = days.map((d, i) => (d ? i : -1)).filter((i) => i >= 0);
  if (on.length === 7) return 'Every day';
  if (on.length === 0) return 'No days';
  if (days.every((d, i) => d === WEEKDAYS[i])) return 'Mon–Fri';
  if (on.length === 2 && days[0] && days[6]) return 'Weekends';
  return on.map((i) => DAY_NAMES[i]).join(', ');
}

export function scheduleText(sc) {
  const time = sc.from === sc.to ? 'all day' : `${fmtTime(sc.from)}–${fmtTime(sc.to)}`;
  return `${daysText(sc.days)}, ${time}`;
}

// A schedule the user never touched comes back from the backend as all zeros.
const blank = (sc) => !sc || !sc.days || (!sc.days.some(Boolean) && sc.from === 0 && sc.to === 0);

export function ScheduleEditor({ rule, inForce, onRule }) {
  const sc = blank(rule.schedule) ? { ...DEFAULT_SCHEDULE, on: !!(rule.schedule && rule.schedule.on) } : rule.schedule;
  const set = (patch) => onRule({ schedule: { ...sc, ...patch } });
  return (
    <>
      <div class="rule">
        <span class="rule-name"><Icon name="clock" size={14} />Only at certain times</span>
        <span class="rule-ctl">
          <Switch checked={sc.on} onChange={(v) => onRule({ schedule: v ? { ...(blank(rule.schedule) ? DEFAULT_SCHEDULE : rule.schedule), on: true } : { ...sc, on: false } })} />
        </span>
      </div>
      {sc.on && (
        <div class="schedule">
          <div class="days" role="group" aria-label="Days">
            {DAY_LETTERS.map((l, i) => (
              <button type="button" class={sc.days[i] ? 'on' : ''} title={DAY_NAMES[i]} aria-pressed={!!sc.days[i]}
                onClick={() => { const d = sc.days.slice(); d[i] = !d[i]; set({ days: d }); }}>{l}</button>
            ))}
          </div>
          <div class="times">
            <label>From <input type="time" value={fmtTime(sc.from)} onChange={(e) => set({ from: parseTime(e.currentTarget.value) })} /></label>
            <label>to <input type="time" value={fmtTime(sc.to)} onChange={(e) => set({ to: parseTime(e.currentTarget.value) })} /></label>
          </div>
          <div class={'sched-state' + (inForce ? ' live' : '')}>
            <span class="dot" />
            {inForce ? 'In force now' : 'Not in force right now'} · {scheduleText(sc)}
            {sc.to <= sc.from && sc.from !== sc.to ? ' (overnight)' : ''}
          </div>
        </div>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// New-app prompts
// ---------------------------------------------------------------------------

export function PendingCards({ pending, apply }) {
  if (!pending || pending.length === 0) return null;
  const shown = pending.slice(0, 3);
  return (
    <div class="prompts" role="region" aria-label="Apps waiting for approval">
      {shown.map((p) => (
        <div class="prompt" key={p.id} role="alertdialog" aria-label={`${p.name} wants to connect`}>
          <div class="prompt-head">
            <span class="appicon tile" style={{ width: '32px', height: '32px', fontSize: '17px', background: `hsl(${hueOf(p.name)} 42% 34%)` }}>
              {(p.name || '?').charAt(0).toUpperCase()}
            </span>
            <div class="grow">
              <div class="prompt-title">{p.name} wants to connect</div>
              <div class="prompt-path" title={p.path}>{p.path || 'Unknown location'}</div>
            </div>
          </div>
          <div class="prompt-dest">
            <Icon name="globe" size={13} />
            {p.remote ? <span>{p.host || p.remote}{p.port ? ':' + p.port : ''}</span> : <span class="dim">Waiting for its first connection...</span>}
          </div>
          <div class="prompt-note">Its traffic is on hold until you choose. Your choice is remembered.</div>
          <div class="prompt-actions">
            <button type="button" class="btn danger" onClick={() => apply(call('decide', { id: p.id, allow: false }))}>
              <Icon name="block" size={14} />Block
            </button>
            <button type="button" class="btn primary" onClick={() => apply(call('decide', { id: p.id, allow: true }))}>
              <Icon name="check" size={14} />Allow
            </button>
          </div>
        </div>
      ))}
      {pending.length > shown.length && <div class="prompt-more">+{pending.length - shown.length} more waiting</div>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Connection log
// ---------------------------------------------------------------------------

const timeOf = (ms) => new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });

export function ConnectionsTab({ search, onPickApp }) {
  const [rows, setRows] = useState([]);
  const [blockedOnly, setBlockedOnly] = useState(false);
  const [paused, setPaused] = useState(false);
  const seq = useRef(0);
  const pausedRef = useRef(false);
  pausedRef.current = paused;

  useEffect(() => {
    let alive = true;
    const poll = async () => {
      if (pausedRef.current) return;
      try {
        const add = await call('log', { since: seq.current });
        if (!alive || !add.length) return;
        seq.current = add[add.length - 1].seq;
        setRows((r) => {
          const next = r.concat(add);
          return next.length > 1500 ? next.slice(next.length - 1500) : next;
        });
      } catch (e) { /* the status bar shows backend errors */ }
    };
    poll();
    const t = setInterval(poll, 1000);
    return () => { alive = false; clearInterval(t); };
  }, []);

  const q = search.trim().toLowerCase();
  const list = useMemo(() => {
    const out = [];
    for (let i = rows.length - 1; i >= 0 && out.length < 400; i--) {
      const r = rows[i];
      if (blockedOnly && !r.blocked) continue;
      if (q && !(`${r.app} ${r.host} ${r.remote} ${r.port} ${r.site}`.toLowerCase().includes(q))) continue;
      out.push(r);
    }
    return out;
  }, [rows, blockedOnly, q]);
  const blocked = rows.reduce((n, r) => n + (r.blocked ? 1 : 0), 0);

  return (
    <div class="page log">
      <div class="page-head">
        <div>
          <h2>Connections</h2>
          <p>Every new connection, newest first. Names come from the DNS lookups your PC makes.</p>
        </div>
        <div class="head-actions">
          <Switch label={`Blocked only (${blocked})`} checked={blockedOnly} onChange={setBlockedOnly} />
          <button type="button" class="btn" onClick={() => setPaused(!paused)}>{paused ? 'Resume' : 'Pause'}</button>
          <button type="button" class="btn" onClick={() => setRows([])}><Icon name="trash" size={14} />Clear</button>
        </div>
      </div>
      <div class="table logtable">
        <div class="trow thead2">
          <span class="l-time">Time</span><span class="l-app">App</span><span class="grow">Destination</span>
          <span class="l-port">Port</span><span class="l-state">Result</span>
        </div>
        <div class="logbody">
          {list.map((r) => (
            <div class={'trow' + (r.blocked ? ' blocked' : '')} key={r.seq}>
              <span class="l-time dim">{timeOf(r.time)}</span>
              <button type="button" class="l-app link-plain" title={'Show ' + r.app} onClick={() => onPickApp(r.appId)}>{r.app}</button>
              <span class="grow ellip" title={r.remote}>
                {r.host ? <><b>{r.host}</b> <span class="dim">{r.remote}</span></> : r.remote || 'unknown'}
                {r.site && <span class="badge small"><Icon name="globe" size={11} />{r.site}</span>}
              </span>
              <span class="l-port dim">{r.proto} {r.port}</span>
              <span class="l-state">{r.blocked ? <span class="badge danger">Blocked</span> : <span class="pill ok">Allowed</span>}</span>
            </div>
          ))}
          {list.length === 0 && <div class="empty big">{rows.length ? 'Nothing matches.' : 'No connections yet.'}</div>}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Website rules
// ---------------------------------------------------------------------------

const EMPTY_RULE = { blockIn: false, blockOut: false, limitIn: { on: false, bps: 0 }, limitOut: { on: false, bps: 0 }, priority: 1, quota: { on: false, bytes: 0 } };

export function WebsitesTab({ snap, units, apply }) {
  const [presets, setPresets] = useState([]);
  const [name, setName] = useState('');
  const [patterns, setPatterns] = useState('');
  const [action, setAction] = useState('limit');
  const [speed, setSpeed] = useState(512 * KB);
  const [error, setError] = useState('');
  useEffect(() => { call('sitePresets').then(setPresets).catch(() => {}); }, []);

  const add = () => {
    const rule = action === 'block'
      ? { ...EMPTY_RULE, blockIn: true, blockOut: true }
      : { ...EMPTY_RULE, limitIn: { on: true, bps: speed }, limitOut: { on: true, bps: speed } };
    call('setSite', { site: { name, patterns: [patterns] }, rule })
      .then((s) => { setName(''); setPatterns(''); setError(''); apply(Promise.resolve(s)); })
      .catch((e) => setError(String(e.message || e)));
  };
  const save = (s, patch) => apply(call('setSite', { site: { id: s.id, name: s.name, patterns: s.patterns }, rule: { ...s.rule, ...patch } }));

  return (
    <div class="page">
      <div class="page-head">
        <div>
          <h2>Websites</h2>
          <p>Block or slow down a website for every app at once. Rules match the site and its subdomains, an IP address, or a range like 203.0.113.0/24.</p>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">Add a website rule</div></div>
        <div class="preset-row">
          {presets.map((p) => (
            <button type="button" class="chip" onClick={() => { setName(p.name); setPatterns(p.patterns.join(', ')); }}>{p.name}</button>
          ))}
        </div>
        <div class="site-form">
          <label class="field">
            <span>Name</span>
            <input type="text" placeholder="e.g. YouTube" value={name} onInput={(e) => setName(e.currentTarget.value)} />
          </label>
          <label class="field grow">
            <span>Websites, IP addresses or ranges</span>
            <input type="text" placeholder="youtube.com, googlevideo.com" value={patterns} onInput={(e) => setPatterns(e.currentTarget.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') add(); }} />
          </label>
        </div>
        <div class="site-form">
          <label class="field">
            <span>Action</span>
            <select value={action} onChange={(e) => setAction(e.currentTarget.value)}>
              <option value="limit">Limit speed</option>
              <option value="block">Block</option>
            </select>
          </label>
          {action === 'limit' && (
            <label class="field">
              <span>Speed (each way, shared by all apps)</span>
              <RateField label="Website speed" value={speed} onCommit={setSpeed} />
            </label>
          )}
          <span class="grow" />
          <button type="button" class="btn primary" disabled={!patterns.trim()} onClick={add}><Icon name="plus" size={14} />Add rule</button>
        </div>
        {error && <div class="hint warn pad">{error}</div>}
      </div>

      {snap.sites.length === 0 ? (
        <div class="empty">No website rules yet. Pick a preset above to start.</div>
      ) : snap.sites.map((s) => {
        const r = s.rule;
        const blocked = r.blockIn && r.blockOut;
        return (
          <div class="card site">
            <div class="card-head">
              <div class="grow">
                <div class="card-title"><Icon name="globe" size={14} /> {s.name}</div>
                <div class="patterns">{s.patterns.map((p) => <span class="chip static">{p}</span>)}</div>
              </div>
              <div class="site-live">
                <span class="dl"><Icon name="down" size={12} />{fmtRate(s.rateIn, units)}</span>
                <span class="ul"><Icon name="up" size={12} />{fmtRate(s.rateOut, units)}</span>
                <span class="dim">{fmtBytes(s.totalIn + s.totalOut)} this session{s.dropped ? `, ${fmtBytes(s.dropped)} held back` : ''}</span>
              </div>
              <button type="button" class="btn icononly" title="Delete rule" aria-label={'Delete rule ' + s.name}
                onClick={() => apply(call('deleteSite', { id: s.id }))}><Icon name="trash" size={14} /></button>
            </div>
            <div class="rule">
              <span class="rule-name"><Icon name="block" size={14} />Block completely</span>
              <Switch checked={blocked} onChange={(v) => save(s, { blockIn: v, blockOut: v })} />
            </div>
            {!blocked && (
              <>
                <div class="rule">
                  <span class="rule-name"><Icon name="down" size={14} />Limit download</span>
                  <span class="rule-ctl">
                    <RateField label="Download limit" value={r.limitIn.bps || 512 * KB} disabled={!r.limitIn.on} onCommit={(v) => save(s, { limitIn: { on: true, bps: v } })} />
                    <Switch checked={r.limitIn.on} onChange={(v) => save(s, { limitIn: { on: v, bps: r.limitIn.bps || 512 * KB } })} />
                  </span>
                </div>
                <div class="rule">
                  <span class="rule-name"><Icon name="up" size={14} />Limit upload</span>
                  <span class="rule-ctl">
                    <RateField label="Upload limit" value={r.limitOut.bps || 512 * KB} disabled={!r.limitOut.on} onCommit={(v) => save(s, { limitOut: { on: true, bps: v } })} />
                    <Switch checked={r.limitOut.on} onChange={(v) => save(s, { limitOut: { on: v, bps: r.limitOut.bps || 512 * KB } })} />
                  </span>
                </div>
              </>
            )}
            <ScheduleEditor rule={r} inForce={s.inForce} onRule={(patch) => save(s, patch)} />
          </div>
        );
      })}
      <p class="footnote">
        Matching by name needs the normal DNS lookups Windows makes. If a browser uses "secure DNS" (DNS over HTTPS),
        add the site's IP range instead, or turn secure DNS off in that browser.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Window and startup settings (Windows only)
// ---------------------------------------------------------------------------

export function WindowSettings() {
  const [st, setSt] = useState(null);
  const [err, setErr] = useState('');
  useEffect(() => { call('shellPrefs', {}).then(setSt).catch(() => setSt(false)); }, []);
  if (!st) return null;
  const change = (patch) => call('shellPrefs', patch).then((v) => { setSt(v); setErr(''); }).catch((e) => setErr(String(e.message || e)));
  return (
    <div class="card">
      <div class="card-head"><div class="card-title">Window and startup</div></div>
      <div class="rule"><span class="rule-name tall">Start with Windows<small>Starts in the notification area when you sign in, without a permission prompt.</small></span>
        <Switch checked={st.startup} onChange={(v) => change({ startup: v })} /></div>
      <div class="rule"><span class="rule-name tall">Close button keeps it running<small>Closing the window hides it to the notification area so your limits stay on. Quit from the tray icon.</small></span>
        <Switch checked={st.closeToTray} onChange={(v) => change({ closeToTray: v })} /></div>
      {err && <div class="hint warn pad">{err}</div>}
    </div>
  );
}
