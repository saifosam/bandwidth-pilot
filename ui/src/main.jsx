import { render } from 'preact';
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { call } from './api.js';
import { fmtRate, fmtBytes, hueOf, PRIORITIES, KB, MB } from './format.js';
import { Icon, Logo } from './icons.jsx';
import { Switch, Segmented, RateField, SizeField } from './controls.jsx';
import { Sparkline, RateChart, UsageBars } from './charts.jsx';
import { ScheduleEditor, PendingCards, ConnectionsTab, WebsitesTab, WindowSettings, scheduleText, daysText, fmtTime } from './features.jsx';

const HOST = '__host';
const GROUPS = [
  { kind: 'app', title: 'Apps', note: 'have a window open' },
  { kind: 'background', title: 'Background processes', note: 'no window: tray apps, updaters, helpers' },
  { kind: 'windows', title: 'Windows processes', note: 'part of Windows' },
];
const KIND_LABEL = { app: 'App', background: 'Background process', windows: 'Windows process' };
const DEFAULT_LIMIT = 512 * KB;
const DEFAULT_QUOTA = 1024 * MB;
const PRESETS = [
  { label: '100 KB/s', bps: 100 * KB },
  { label: '500 KB/s', bps: 500 * KB },
  { label: '1 MB/s', bps: MB },
  { label: '5 MB/s', bps: 5 * MB },
];

// ---------------------------------------------------------------------------
// Small pieces
// ---------------------------------------------------------------------------

function AppIcon({ app, icons, size = 20 }) {
  const url = icons.get(app.id);
  if (url) return <img class="appicon" src={url} width={size} height={size} alt="" />;
  const letter = (app.name || '?').trim().charAt(0).toUpperCase() || '?';
  return (
    <span class="appicon tile" style={{ width: size + 'px', height: size + 'px', fontSize: Math.round(size * 0.55) + 'px', background: `hsl(${hueOf(app.name)} 42% 34%)` }}>
      {letter}
    </span>
  );
}

// The little pills in the "Rules" column.
function RuleBadges({ app, config, units }) {
  const r = app.rule;
  const out = [];
  if (r.blockIn || r.blockOut) {
    const what = r.blockIn && r.blockOut ? 'Blocked' : r.blockIn ? 'Download blocked' : 'Upload blocked';
    out.push(<span class={'badge danger' + (config.blocker ? '' : ' off')} title={config.blocker ? what : what + ' (Blocker is switched off)'}><Icon name="block" size={12} />{what}</span>);
  }
  if (r.limitIn.on) out.push(<span class={'badge' + (config.limiter ? '' : ' off')} title={'Download limit' + (config.limiter ? '' : ' (Limiter is switched off)')}><Icon name="down" size={12} />{fmtRate(r.limitIn.bps, units)}</span>);
  if (r.limitOut.on) out.push(<span class={'badge' + (config.limiter ? '' : ' off')} title={'Upload limit' + (config.limiter ? '' : ' (Limiter is switched off)')}><Icon name="up" size={12} />{fmtRate(r.limitOut.bps, units)}</span>);
  if (r.priority !== 1) out.push(<span class={'badge' + (config.priorities ? '' : ' off')} title={'Priority' + (config.priorities ? '' : ' (Priorities are switched off)')}><Icon name="flag" size={12} />{PRIORITIES[r.priority]}</span>);
  if (r.schedule && r.schedule.on) {
    out.push(<span class={'badge' + (app.inForce ? '' : ' off soft')} title={(app.inForce ? 'In force now: ' : 'Not in force right now: ') + scheduleText(r.schedule)}>
      <Icon name="clock" size={12} />{r.schedule.from === r.schedule.to ? daysText(r.schedule.days) : `${fmtTime(r.schedule.from)}–${fmtTime(r.schedule.to)}`}</span>);
  }
  if (r.quota.on) {
    const pct = Math.min(100, Math.round(((app.todayIn + app.todayOut) / Math.max(1, r.quota.bytes)) * 100));
    out.push(<span class={'badge' + (app.quotaHit ? ' danger' : '')} title="Daily data quota"><Icon name="quota" size={12} />{app.quotaHit ? 'Quota reached' : pct + '% of quota'}</span>);
  }
  return <span class="badges">{out}</span>;
}

function Stat({ label, value, sub }) {
  return (
    <div class="stat">
      <div class="stat-label">{label}</div>
      <div class="stat-value">{value}</div>
      {sub && <div class="stat-sub">{sub}</div>}
    </div>
  );
}

function Legend({ hist, units }) {
  const last = (a) => (a && a.length ? a[a.length - 1] : 0);
  return (
    <div class="legend">
      <span class="key dl"><i />Download <b>{fmtRate(last(hist && hist.in), units)}</b></span>
      <span class="key ul"><i />Upload <b>{fmtRate(last(hist && hist.out), units)}</b></span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Activity list
// ---------------------------------------------------------------------------

function Activity({ snap, view, setView, selected, setSelected, expanded, toggleExpand, conns, hist, icons, units, onRule, onMenu }) {
  const { filter, sort, search, group } = view;
  const [closed, setClosed] = useState(() => new Set());
  const q = search.trim().toLowerCase();
  const children = useMemo(() => {
    const m = new Map();
    for (const a of snap.apps) {
      if (!a.parent) continue;
      if (!m.has(a.parent)) m.set(a.parent, []);
      m.get(a.parent).push(a);
    }
    for (const list of m.values()) list.sort((a, b) => (b.rateIn + b.rateOut) - (a.rateIn + a.rateOut) || a.name.localeCompare(b.name));
    return m;
  }, [snap]);
  const rows = useMemo(() => {
    const tops = snap.apps.filter((a) => !a.parent).map((a) => {
      const kids = children.get(a.id);
      if (!kids) return a;
      return { ...a, rateIn: a.rateIn + kids.reduce((n, k) => n + k.rateIn, 0), rateOut: a.rateOut + kids.reduce((n, k) => n + k.rateOut, 0),
        online: a.online || kids.some((k) => k.online), hasRule: a.hasRule || kids.some((k) => k.hasRule) };
    });
    let list = tops.filter((a) => {
      if (q && !a.name.toLowerCase().includes(q) && !a.path.toLowerCase().includes(q)) return false;
      if (filter === 'online') return a.online;
      if (filter === 'offline') return !a.online;
      if (filter === 'rules') return a.hasRule;
      return true;
    });
    const dir = sort.desc ? -1 : 1;
    const key = { name: (a) => a.name.toLowerCase(), dl: (a) => a.rateIn, ul: (a) => a.rateOut }[sort.key];
    list = list.slice().sort((a, b) => {
      const x = key(a), y = key(b);
      if (x < y) return -dir;
      if (x > y) return dir;
      return a.name.localeCompare(b.name);
    });
    return list;
  }, [snap, children, filter, sort, q]);

  const sections = group
    ? GROUPS.map((g) => ({ ...g, rows: rows.filter((a) => a.kind === g.kind) })).filter((g) => g.rows.length)
    : [{ kind: 'all', rows }];
  const toggleGroup = (kind) => setClosed((c) => {
    const n = new Set(c);
    if (n.has(kind)) n.delete(kind); else n.add(kind);
    return n;
  });

  const renderRow = (a, child) => {
    const open = expanded.has(a.id);
    const idle = a.rateIn + a.rateOut === 0;
    const h = hist.get(a.id);
    const blocked = a.rule.blockIn && a.rule.blockOut;
    return (
      <div key={a.id}>
        <div class={'row' + (child ? ' child' : '') + (selected === a.id ? ' selected' : '') + (a.online ? '' : ' offline') + (a.hasRule && !a.inForce && a.rule.schedule && a.rule.schedule.on ? ' dormant' : '')}
          role="option" aria-selected={selected === a.id}
          onClick={() => setSelected(a.id)}
          onContextMenu={(e) => { e.preventDefault(); setSelected(a.id); onMenu({ x: e.clientX, y: e.clientY, id: a.id }); }}>
          <span class="c-name">
            <button type="button" class={'twisty' + (open ? ' open' : '')} aria-label={open ? 'Hide connections' : 'Show connections'}
              onClick={(e) => { e.stopPropagation(); toggleExpand(a.id); }}>
              <Icon name="chevron" size={12} />
            </button>
            {child ? <span class="procicon"><Icon name="split" size={13} /></span> : <AppIcon app={a} icons={icons} />}
            <span class="name" title={a.path || a.name}>{a.name}</span>
            {a.split && <span class="gcount" title="Shown per process">{(children.get(a.id) || []).length} processes</span>}
          </span>
          <span class={'c-num dl' + (a.rateIn === 0 ? ' zero' : '')}>{fmtRate(a.rateIn, units)}</span>
          <span class={'c-num ul' + (a.rateOut === 0 ? ' zero' : '')}>{fmtRate(a.rateOut, units)}</span>
          <span class={'c-spark' + (idle ? ' zero' : '')}><Sparkline data={h && h.sum} /></span>
          <span class="c-rules">
            <RuleBadges app={a} config={snap.config} units={units} />
            <span class="quick">
              <button type="button" title={a.rule.limitIn.on ? 'Remove download limit' : 'Limit download'} class={a.rule.limitIn.on ? 'on' : ''}
                onClick={(e) => { e.stopPropagation(); onRule(a, { limitIn: { on: !a.rule.limitIn.on, bps: a.rule.limitIn.bps || DEFAULT_LIMIT } }); }}>
                <Icon name="down" size={14} />
              </button>
              <button type="button" title={a.rule.limitOut.on ? 'Remove upload limit' : 'Limit upload'} class={a.rule.limitOut.on ? 'on' : ''}
                onClick={(e) => { e.stopPropagation(); onRule(a, { limitOut: { on: !a.rule.limitOut.on, bps: a.rule.limitOut.bps || DEFAULT_LIMIT } }); }}>
                <Icon name="up" size={14} />
              </button>
              <button type="button" title={blocked ? 'Unblock' : 'Block all traffic'} class={blocked ? 'on danger' : ''}
                onClick={(e) => { e.stopPropagation(); onRule(a, { blockIn: !blocked, blockOut: !blocked }); }}>
                <Icon name="block" size={14} />
              </button>
            </span>
          </span>
        </div>
        {open && <ConnRows list={conns.get(a.id)} units={units} />}
      </div>
    );
  };

  const head = (key, label, cls) => (
    <button type="button" class={'th ' + cls + (sort.key === key ? ' sorted' : '')}
      onClick={() => setView({ sort: { key, desc: sort.key === key ? !sort.desc : key !== 'name' } })}>
      {label}{sort.key === key && <Icon name={sort.desc ? 'down' : 'up'} size={11} />}
    </button>
  );
  const counts = {
    all: snap.apps.length,
    online: snap.apps.filter((a) => a.online).length,
    offline: snap.apps.filter((a) => !a.online).length,
    rules: snap.apps.filter((a) => a.hasRule).length,
  };

  return (
    <div class="activity">
      <div class="toolbar">
        <Segmented small value={filter} onChange={(v) => setView({ filter: v })} options={[
          { value: 'all', label: `All ${counts.all}` },
          { value: 'online', label: `Online ${counts.online}` },
          { value: 'offline', label: `Offline ${counts.offline}` },
          { value: 'rules', label: `With rules ${counts.rules}` },
        ]} />
        <Switch label="Group by type" checked={group} onChange={(v) => setView({ group: v })} />
      </div>
      <div class="thead">
        {head('name', 'Name', 'c-name')}
        {head('dl', 'Download', 'c-num')}
        {head('ul', 'Upload', 'c-num')}
        <span class="th c-spark">Last minute</span>
        <span class="th c-rules">Rules</span>
      </div>
      <div class="tbody" role="listbox" aria-label="Applications">
        <div class={'row host' + (selected === HOST ? ' selected' : '')} role="option" aria-selected={selected === HOST}
          onClick={() => setSelected(HOST)}>
          <span class="c-name"><span class="twisty" /><span class="appicon hosticon"><Icon name="monitor" size={16} /></span><span class="name">{snap.host}</span></span>
          <span class="c-num dl">{fmtRate(snap.rateIn, units)}</span>
          <span class="c-num ul">{fmtRate(snap.rateOut, units)}</span>
          <span class="c-spark"><Sparkline data={hist.get(HOST) && hist.get(HOST).sum} /></span>
          <span class="c-rules">
            {snap.config.capOn && snap.config.limiter && (
              <span class="badges"><span class="badge" title="Total bandwidth cap"><Icon name="gauge" size={12} />Cap {fmtRate(snap.config.capIn, units)} / {fmtRate(snap.config.capOut, units)}</span></span>
            )}
          </span>
        </div>
        {sections.map((sec) => {
          const open = !closed.has(sec.kind);
          const sumIn = sec.rows.reduce((n, a) => n + a.rateIn, 0);
          const sumOut = sec.rows.reduce((n, a) => n + a.rateOut, 0);
          return [
            group && (
              <div key={'g-' + sec.kind} class="row grouphead" onClick={() => toggleGroup(sec.kind)}
                role="button" aria-expanded={open}>
                <span class="c-name">
                  <span class={'twisty' + (open ? ' open' : '')}><Icon name="chevron" size={12} /></span>
                  <span class="name">{sec.title}</span><span class="gcount">{sec.rows.length}</span>
                  <span class="gnote">{sec.note}</span>
                </span>
                <span class={'c-num' + (sumIn === 0 ? ' zero' : '')}>{fmtRate(sumIn, units)}</span>
                <span class={'c-num' + (sumOut === 0 ? ' zero' : '')}>{fmtRate(sumOut, units)}</span>
                <span class="c-spark" />
                <span class="c-rules" />
              </div>
            ),
            ...(open ? sec.rows.flatMap((a) => [renderRow(a, false), ...(a.split ? (children.get(a.id) || []).map((k) => renderRow(k, true)) : [])]) : []),
          ];
        })}
        {rows.length === 0 && <div class="empty">{q ? `Nothing matches "${search}".` : 'No apps in this view.'}</div>}
      </div>
    </div>
  );
}

function ConnRows({ list, units }) {
  if (!list) return <div class="row conn muted"><span class="c-name">Loading connections...</span></div>;
  if (list.length === 0) return <div class="row conn muted"><span class="c-name">No open connections.</span></div>;
  return list.slice(0, 40).map((c) => (
    <div class="row conn">
      <span class="c-name">
        <span class="proto">{c.proto}</span>
        <span class="addr" title={c.remote}>{c.host || (c.remote ? (c.remote.includes(':') ? `[${c.remote}]` : c.remote) : 'unknown')}:{c.remotePort}</span>
        <span class="dim">local port {c.localPort}{c.pid ? ` · PID ${c.pid}` : ''}</span>
      </span>
      <span class={'c-num' + (c.rateIn === 0 ? ' zero' : '')}>{fmtRate(c.rateIn, units)}</span>
      <span class={'c-num' + (c.rateOut === 0 ? ' zero' : '')}>{fmtRate(c.rateOut, units)}</span>
      <span class="c-spark" />
      <span class="c-rules dim">{fmtBytes(c.in + c.out)} so far</span>
    </div>
  ));
}

// ---------------------------------------------------------------------------
// Info panel
// ---------------------------------------------------------------------------

function CapEditor({ config, snap, onConfig, units }) {
  const peakOK = snap.peakIn > 64 * KB;
  return (
    <div class="card">
      <div class="card-head">
        <div>
          <div class="card-title">Total bandwidth cap</div>
          <div class="card-note">Caps all traffic on this PC. Inside the cap, higher-priority apps are served first.</div>
        </div>
        <Switch checked={config.capOn} onChange={(v) => onConfig({ capOn: v, capIn: config.capIn || 5 * MB, capOut: config.capOut || MB })} />
      </div>
      <div class={'field-row' + (config.capOn ? '' : ' disabled')}>
        <span class="field-label"><Icon name="down" size={14} />Download</span>
        <RateField label="Download cap" value={config.capIn || 5 * MB} disabled={!config.capOn} onCommit={(v) => onConfig({ capIn: v })} />
      </div>
      <div class={'field-row' + (config.capOn ? '' : ' disabled')}>
        <span class="field-label"><Icon name="up" size={14} />Upload</span>
        <RateField label="Upload cap" value={config.capOut || MB} disabled={!config.capOn} onCommit={(v) => onConfig({ capOut: v })} />
      </div>
      <button type="button" class="link" disabled={!peakOK}
        title={peakOK ? '' : 'Needs a little traffic first'}
        onClick={() => onConfig({ capOn: true, capIn: Math.round(snap.peakIn * 0.9), capOut: Math.round(Math.max(snap.peakOut, 64 * KB) * 0.9) })}>
        Set to 90% of the fastest speed seen ({fmtRate(snap.peakIn, units)} down, {fmtRate(snap.peakOut, units)} up)
      </button>
      {config.capOn && !config.limiter && <div class="hint warn">The Limiter is switched off, so this cap is not applied.</div>}
    </div>
  );
}

function HostInfo({ snap, hist, units, onConfig }) {
  const conns = snap.apps.reduce((n, a) => n + a.conns, 0);
  return (
    <div class="info-body">
      <div class="info-head">
        <span class="appicon hosticon big"><Icon name="monitor" size={22} /></span>
        <div>
          <div class="info-title">{snap.host}</div>
          <div class="info-sub">All traffic on this PC</div>
        </div>
      </div>
      <Legend hist={hist.get(HOST)} units={units} />
      <RateChart hist={hist.get(HOST)} units={units} />
      <CapEditor config={snap.config} snap={snap} onConfig={onConfig} units={units} />
      <div class="stats">
        <Stat label="Downloaded" value={fmtBytes(snap.totalIn)} sub="this session" />
        <Stat label="Uploaded" value={fmtBytes(snap.totalOut)} sub="this session" />
        <Stat label="Fastest download" value={fmtRate(snap.peakIn, units)} />
        <Stat label="Fastest upload" value={fmtRate(snap.peakOut, units)} />
        <Stat label="Apps online" value={snap.apps.filter((a) => a.online).length} />
        <Stat label="Connections" value={conns} />
      </div>
    </div>
  );
}

function AppInfo({ app, snap, hist, conns, icons, units, onRule, onClear, onReveal, onSplit }) {
  const r = app.rule;
  const cfg = snap.config;
  const used = app.todayIn + app.todayOut;
  const pct = r.quota.on ? Math.min(100, (used / Math.max(1, r.quota.bytes)) * 100) : 0;
  return (
    <div class="info-body">
      <div class="info-head">
        <AppIcon app={app} icons={icons} size={36} />
        <div class="grow">
          <div class="info-title">{app.name}</div>
          <div class="info-sub" title={app.path}>{app.path || 'No file path available'}</div>
        </div>
        <span class="pills">
          <span class={'pill ' + (app.online ? 'ok' : '')}>{app.online ? 'Online' : 'Offline'}</span>
          <span class="pill">{KIND_LABEL[app.kind] || 'Process'}</span>
        </span>
      </div>
      <Legend hist={hist.get(app.id)} units={units} />
      <RateChart hist={hist.get(app.id)} units={units} />

      <div class="card">
        <div class="card-head"><div class="card-title">Rules</div>
          {app.hasRule && <button type="button" class="link" onClick={() => onClear(app)}>Remove all</button>}
        </div>

        <div class="rule">
          <span class="rule-name"><Icon name="block" size={14} />Block</span>
          <span class="rule-ctl">
            <Switch label="Download" checked={r.blockIn} onChange={(v) => onRule(app, { blockIn: v })} />
            <Switch label="Upload" checked={r.blockOut} onChange={(v) => onRule(app, { blockOut: v })} />
          </span>
        </div>
        {(r.blockIn || r.blockOut) && !cfg.blocker && <div class="hint warn">The Blocker is switched off at the top, so this is not applied.</div>}

        <div class="rule">
          <span class="rule-name"><Icon name="down" size={14} />Limit download</span>
          <span class="rule-ctl">
            <RateField label="Download limit" value={r.limitIn.bps || DEFAULT_LIMIT} disabled={!r.limitIn.on}
              onCommit={(v) => onRule(app, { limitIn: { on: true, bps: v } })} />
            <Switch checked={r.limitIn.on} onChange={(v) => onRule(app, { limitIn: { on: v, bps: r.limitIn.bps || DEFAULT_LIMIT } })} />
          </span>
        </div>
        <div class="rule">
          <span class="rule-name"><Icon name="up" size={14} />Limit upload</span>
          <span class="rule-ctl">
            <RateField label="Upload limit" value={r.limitOut.bps || DEFAULT_LIMIT} disabled={!r.limitOut.on}
              onCommit={(v) => onRule(app, { limitOut: { on: true, bps: v } })} />
            <Switch checked={r.limitOut.on} onChange={(v) => onRule(app, { limitOut: { on: v, bps: r.limitOut.bps || DEFAULT_LIMIT } })} />
          </span>
        </div>
        {(r.limitIn.on || r.limitOut.on) && !cfg.limiter && <div class="hint warn">The Limiter is switched off at the top, so limits are not applied.</div>}

        <div class="rule">
          <span class="rule-name"><Icon name="flag" size={14} />Priority</span>
          <span class="rule-ctl">
            <Segmented small value={r.priority} onChange={(v) => onRule(app, { priority: v })}
              options={PRIORITIES.map((label, value) => ({ label, value }))} />
          </span>
        </div>
        {r.priority !== 1 && !(cfg.capOn && cfg.limiter && cfg.priorities) && (
          <div class="hint">
            {!cfg.priorities ? 'Priorities are switched off at the top.'
              : 'Priorities decide who goes first inside the total bandwidth cap. Select your PC at the top of the list to set one.'}
          </div>
        )}

        <div class="rule">
          <span class="rule-name"><Icon name="quota" size={14} />Daily quota</span>
          <span class="rule-ctl">
            <SizeField label="Daily quota" value={r.quota.bytes || DEFAULT_QUOTA} disabled={!r.quota.on}
              onCommit={(v) => onRule(app, { quota: { on: true, bytes: v } })} />
            <Switch checked={r.quota.on} onChange={(v) => onRule(app, { quota: { on: v, bytes: r.quota.bytes || DEFAULT_QUOTA } })} />
          </span>
        </div>
        {r.quota.on && (
          <div class="quota">
            <div class="bar"><span class={app.quotaHit ? 'full' : ''} style={{ width: pct + '%' }} /></div>
            <div class="quota-text">
              {app.quotaHit ? 'Quota reached. Blocked until midnight.' : `${fmtBytes(used)} of ${fmtBytes(r.quota.bytes)} used today`}
            </div>
          </div>
        )}
        <ScheduleEditor rule={r} inForce={app.inForce} onRule={(patch) => onRule(app, patch)} />
        {app.parent && <div class="hint pad">This rule applies to this one process and ends when it closes.</div>}
      </div>

      {!app.parent && (
        <div class="card">
          <div class="rule first">
            <span class="rule-name tall">Show each process separately
              <small>Give each running copy of {app.name} its own row and its own rules.</small></span>
            <Switch checked={app.split} onChange={(v) => onSplit(app, v)} />
          </div>
        </div>
      )}

      <div class="stats">
        <Stat label="Downloaded" value={fmtBytes(app.totalIn)} sub="this session" />
        <Stat label="Uploaded" value={fmtBytes(app.totalOut)} sub="this session" />
        <Stat label="Today" value={fmtBytes(used)} sub="down + up" />
        <Stat label="Held back" value={fmtBytes(app.droppedIn + app.droppedOut)} sub="blocked or dropped" />
        <Stat label="Connections" value={app.conns} />
        <Stat label="Processes" value={app.pids.length || (app.online ? 1 : 0)} sub={app.pids.length ? 'PID ' + app.pids.slice(0, 4).join(', ') + (app.pids.length > 4 ? '...' : '') : ''} />
      </div>

      {conns && conns.length > 0 && (
        <div class="card">
          <div class="card-head"><div class="card-title">Busiest connections</div></div>
          {conns.slice(0, 6).map((c) => (
            <div class="conn-line">
              <span class="proto">{c.proto}</span>
              <span class="grow ellip" title={c.remote}>{c.host || c.remote || 'unknown'}:{c.remotePort}</span>
              <span class="num dl">{fmtRate(c.rateIn, units)}</span>
              <span class="num ul">{fmtRate(c.rateOut, units)}</span>
            </div>
          ))}
        </div>
      )}

      {app.path && snap.engine.mode === 'live' && (
        <button type="button" class="btn" onClick={() => onReveal(app)}><Icon name="folder" size={14} />Open file location</button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Other tabs
// ---------------------------------------------------------------------------

function RulesTab({ snap, icons, units, onClear, onClearAll, onEdit }) {
  const list = snap.apps.filter((a) => a.hasRule);
  return (
    <div class="page">
      <div class="page-head">
        <div>
          <h2>Rules</h2>
          <p>Every app with a block, limit, priority or quota. Rules stay saved while an app is closed.</p>
        </div>
        {list.length > 0 && <button type="button" class="btn" onClick={onClearAll}><Icon name="trash" size={14} />Remove all rules</button>}
      </div>
      {list.length === 0 ? (
        <div class="empty big">No rules yet. Pick an app on the Activity tab, or right-click it, to add one.</div>
      ) : (
        <div class="table">
          {list.map((a) => (
            <div class="trow">
              <AppIcon app={a} icons={icons} size={24} />
              <div class="grow">
                <div class="name">{a.name}</div>
                <RuleBadges app={a} config={snap.config} units={units} />
              </div>
              <span class={'pill ' + (a.online ? 'ok' : '')}>{a.online ? 'Online' : 'Offline'}</span>
              <button type="button" class="btn" onClick={() => onEdit(a)}>Edit</button>
              <button type="button" class="btn icononly" title="Remove rules" aria-label={'Remove rules for ' + a.name} onClick={() => onClear(a)}><Icon name="trash" size={14} /></button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function UsageTab({ onReset }) {
  const [rep, setRep] = useState(null);
  const load = () => call('usage').then(setRep).catch(() => {});
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t); }, []);
  if (!rep) return <div class="page"><div class="empty big">Loading...</div></div>;
  const sum = (k) => rep.apps.reduce((n, a) => n + a[k][0] + a[k][1], 0);
  return (
    <div class="page">
      <div class="page-head">
        <div>
          <h2>Usage</h2>
          <p>How much data each app has used. Kept for 30 days on this PC.</p>
        </div>
        <button type="button" class="btn" onClick={() => onReset().then(setRep)}><Icon name="trash" size={14} />Clear history</button>
      </div>
      <div class="stats three">
        <Stat label="Today" value={fmtBytes(sum('today'))} />
        <Stat label="Last 7 days" value={fmtBytes(sum('week'))} />
        <Stat label="Last 30 days" value={fmtBytes(sum('month'))} />
      </div>
      <div class="card">
        <div class="card-head">
          <div class="card-title">Per day</div>
          <div class="legend"><span class="key dl"><i />Download</span><span class="key ul"><i />Upload</span></div>
        </div>
        <UsageBars days={rep.days} />
      </div>
      <div class="table usage">
        <div class="trow thead2"><span class="grow">App</span><span class="num">Today</span><span class="num">7 days</span><span class="num">30 days</span><span class="split">Down / up (30 days)</span></div>
        {rep.apps.map((a) => {
          const m = a.month[0] + a.month[1];
          const share = m ? (a.month[0] / m) * 100 : 0;
          return (
            <div class="trow">
              <span class="grow name">{a.name}</span>
              <span class="num">{fmtBytes(a.today[0] + a.today[1])}</span>
              <span class="num">{fmtBytes(a.week[0] + a.week[1])}</span>
              <span class="num strong">{fmtBytes(m)}</span>
              <span class="split" title={`Download ${fmtBytes(a.month[0])}, upload ${fmtBytes(a.month[1])}`}>
                <span class="splitbar"><i class="d" style={{ width: share + '%' }} /><i class="u" style={{ width: 100 - share + '%' }} /></span>
              </span>
            </div>
          );
        })}
        {rep.apps.length === 0 && <div class="empty">Nothing recorded yet.</div>}
      </div>
    </div>
  );
}

function SettingsTab({ snap, units, setUnits, onConfig, onAsk }) {
  const c = snap.config;
  const e = snap.engine;
  return (
    <div class="page narrow">
      <div class="page-head"><div><h2>Settings</h2></div></div>
      <CapEditor config={c} snap={snap} onConfig={onConfig} units={units} />
      <div class="card">
        <div class="card-head"><div class="card-title">New apps</div></div>
        <div class="rule"><span class="rule-name tall">Ask before new apps connect
          <small>The first time an app you have not used before goes online, its traffic is held until you allow or block it. Apps already seen and parts of Windows are not asked about.</small></span>
          <Switch checked={snap.ask} onChange={onAsk} /></div>
      </div>
      <WindowSettings />
      <div class="card">
        <div class="card-head"><div class="card-title">Master switches</div></div>
        <div class="rule"><span class="rule-name tall">Blocker<small>Apply block rules.</small></span><Switch checked={c.blocker} onChange={(v) => onConfig({ blocker: v })} /></div>
        <div class="rule"><span class="rule-name tall">Limiter<small>Apply speed limits and the total bandwidth cap.</small></span><Switch checked={c.limiter} onChange={(v) => onConfig({ limiter: v })} /></div>
        <div class="rule"><span class="rule-name tall">Priorities<small>Serve higher-priority apps first inside the cap.</small></span><Switch checked={c.priorities} onChange={(v) => onConfig({ priorities: v })} /></div>
      </div>
      <div class="card">
        <div class="card-head"><div class="card-title">Display</div></div>
        <div class="rule"><span class="rule-name">Speed units</span>
          <Segmented small value={units} onChange={setUnits} options={[
            { value: 'auto', label: 'Automatic' }, { value: 'kb', label: 'KB/s' }, { value: 'mb', label: 'MB/s' }, { value: 'bits', label: 'Mbit/s' },
          ]} />
        </div>
      </div>
      <div class="card">
        <div class="card-head"><div class="card-title">About</div></div>
        <div class="about">
          <div><span>Version</span><b>Bandwidth Pilot {snap.version}</b></div>
          <div><span>Engine</span><b>{e.mode === 'demo' ? 'Demo (simulated traffic)' : e.driver || 'Not running'}</b></div>
          <div><span>State</span><b>{e.error ? 'Stopped' : e.intercepting ? 'Shaping traffic' : 'Watching only'}</b></div>
        </div>
        <p class="card-note pad">
          While no rule is active, Bandwidth Pilot only watches copies of your traffic and cannot slow anything down.
          Once a limit, block, quota or cap is on, packets pass through Bandwidth Pilot so it can hold or drop them.
          Closing Bandwidth Pilot removes all shaping immediately.
        </p>
      </div>
    </div>
  );
}

// Right-click menu for an app row.
function ContextMenu({ menu, app, onRule, onClear, onReveal, onSplit, onClose, live }) {
  const ref = useRef(null);
  const [pos, setPos] = useState({ left: menu.x, top: menu.y });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ left: Math.max(8, Math.min(menu.x, window.innerWidth - r.width - 8)), top: Math.max(8, Math.min(menu.y, window.innerHeight - r.height - 8)) });
  }, [menu]);
  useEffect(() => {
    const close = (e) => { if (!ref.current || !ref.current.contains(e.target)) onClose(); };
    const key = (e) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('mousedown', close);
    window.addEventListener('keydown', key);
    window.addEventListener('resize', onClose);
    return () => { window.removeEventListener('mousedown', close); window.removeEventListener('keydown', key); window.removeEventListener('resize', onClose); };
  }, []);
  if (!app) return null;
  const r = app.rule;
  const blocked = r.blockIn && r.blockOut;
  const chips = (key) => (
    <div class="chips">
      <button type="button" class={!r[key].on ? 'on' : ''} onClick={() => onRule(app, { [key]: { on: false, bps: r[key].bps } })}>Off</button>
      {PRESETS.map((p) => (
        <button type="button" class={r[key].on && r[key].bps === p.bps ? 'on' : ''} onClick={() => onRule(app, { [key]: { on: true, bps: p.bps } })}>{p.label}</button>
      ))}
    </div>
  );
  return (
    <div class="menu" ref={ref} style={{ left: pos.left + 'px', top: pos.top + 'px' }} role="menu">
      <div class="menu-title">{app.name}</div>
      <button type="button" class="menu-item" role="menuitem" onClick={() => { onRule(app, { blockIn: !blocked, blockOut: !blocked }); onClose(); }}>
        <Icon name="block" size={14} />{blocked ? 'Unblock' : 'Block all traffic'}
      </button>
      <div class="menu-label"><Icon name="down" size={12} />Limit download</div>
      {chips('limitIn')}
      <div class="menu-label"><Icon name="up" size={12} />Limit upload</div>
      {chips('limitOut')}
      <div class="menu-label"><Icon name="flag" size={12} />Priority</div>
      <div class="chips">
        {PRIORITIES.map((label, v) => (
          <button type="button" class={r.priority === v ? 'on' : ''} onClick={() => onRule(app, { priority: v })}>{label}</button>
        ))}
      </div>
      <div class="menu-sep" />
      <button type="button" class="menu-item" role="menuitem" disabled={!app.hasRule} onClick={() => { onClear(app); onClose(); }}>
        <Icon name="trash" size={14} />Remove all rules
      </button>
      {!app.parent && (
        <button type="button" class="menu-item" role="menuitem" onClick={() => { onSplit(app, !app.split); onClose(); }}>
          <Icon name="split" size={14} />{app.split ? 'Show as one row' : 'Show each process separately'}
        </button>
      )}
      {live && app.path && (
        <button type="button" class="menu-item" role="menuitem" onClick={() => { onReveal(app); onClose(); }}>
          <Icon name="folder" size={14} />Open file location
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

function App() {
  const [snap, setSnap] = useState(null);
  const [error, setError] = useState('');
  const [tab, setTab] = useState('activity');
  const [selected, setSelected] = useState(HOST);
  const [expanded, setExpanded] = useState(() => new Set());
  const [view, setViewState] = useState({ filter: 'all', sort: { key: 'dl', desc: true }, search: '', group: true });
  const [units, setUnitsState] = useState('auto');
  const [menu, setMenu] = useState(null);
  const [conns, setConns] = useState(() => new Map());
  const [, bump] = useState(0);
  const hist = useRef(new Map());
  const icons = useRef(new Map());
  const iconAsked = useRef(new Set());
  const lastSeq = useRef(-1);
  const watch = useRef({ selected, expanded });
  watch.current = { selected, expanded };

  const prefs = useRef({});
  const savePrefs = (patch) => { prefs.current = { ...prefs.current, ...patch }; call('setPrefs', { prefs: prefs.current }).catch(() => {}); };
  const setView = (patch) => {
    setViewState((v) => ({ ...v, ...patch }));
    if ('group' in patch) savePrefs({ group: patch.group });
  };
  const setUnits = (u) => { setUnitsState(u); savePrefs({ units: u }); };

  // Take in a new snapshot: extend the rate histories and fetch missing icons.
  const accept = (s) => {
    if (s.seq !== lastSeq.current) {
      lastSeq.current = s.seq;
      const push = (id, rin, rout) => {
        let h = hist.current.get(id);
        if (!h) hist.current.set(id, (h = { in: [], out: [], sum: [] }));
        h.in.push(rin); h.out.push(rout); h.sum.push(rin + rout);
        if (h.in.length > 60) { h.in.shift(); h.out.shift(); h.sum.shift(); }
      };
      push(HOST, s.rateIn, s.rateOut);
      const live = new Set([HOST]);
      const kid = new Map();
      for (const a of s.apps) if (a.parent) {
        const k = kid.get(a.parent) || [0, 0];
        kid.set(a.parent, [k[0] + a.rateIn, k[1] + a.rateOut]);
      }
      for (const a of s.apps) {
        const k = kid.get(a.id) || [0, 0];
        push(a.id, a.rateIn + k[0], a.rateOut + k[1]);
        live.add(a.id);
      }
      for (const id of hist.current.keys()) if (!live.has(id)) hist.current.delete(id);
    }
    if (s.engine.mode === 'live') {
      let budget = 4;
      for (const a of s.apps) {
        if (!a.path || iconAsked.current.has(a.id)) continue;
        if (budget-- <= 0) break;
        iconAsked.current.add(a.id);
        call('icon', { id: a.id }).then((url) => { if (url) { icons.current.set(a.id, url); bump((n) => n + 1); } }).catch(() => {});
      }
    }
    setSnap(s);
  };

  useEffect(() => {
    let alive = true;
    call('prefs').then((p) => {
      if (!alive || !p) return;
      prefs.current = p;
      if (p.units) setUnitsState(p.units);
      if (typeof p.group === 'boolean') setViewState((v) => ({ ...v, group: p.group }));
    }).catch(() => {});
    const poll = async () => {
      try {
        const s = await call('snapshot');
        if (!alive) return;
        accept(s);
        setError('');
        // Refresh connection lists for the rows that show them.
        const want = new Set(watch.current.expanded);
        if (watch.current.selected !== HOST) want.add(watch.current.selected);
        const next = new Map();
        await Promise.all([...want].map(async (id) => { next.set(id, await call('connections', { id })); }));
        if (alive) setConns(next);
      } catch (e) {
        if (alive) setError(String((e && e.message) || e));
      }
    };
    poll();
    const t = setInterval(poll, 1000);
    return () => { alive = false; clearInterval(t); };
  }, []);

  if (!snap) {
    return <div class="splash">{error ? <><b>Bandwidth Pilot could not reach its backend.</b><span>{error}</span></> : 'Starting...'}</div>;
  }

  const apply = (p) => p.then(accept).catch((e) => setError(String((e && e.message) || e)));
  const onRule = (app, patch) => apply(call('setRule', { id: app.id, rule: { ...app.rule, ...patch } }));
  const onClear = (app) => apply(call('clearRule', { id: app.id }));
  const onClearAll = () => apply(call('clearAllRules'));
  const onConfig = (patch) => apply(call('setConfig', { config: { ...snap.config, ...patch } }));
  const onSplit = (app, on) => apply(call('setSplit', { id: app.id, on }));
  const onReveal = (app) => call('reveal', { id: app.id }).catch(() => {});
  const toggleExpand = (id) => setExpanded((s) => {
    const n = new Set(s);
    if (n.has(id)) n.delete(id); else {
      n.add(id);
      call('connections', { id }).then((c) => setConns((m) => new Map(m).set(id, c))).catch(() => {});
    }
    return n;
  });
  const select = (id) => {
    setSelected(id);
    if (id !== HOST && !conns.has(id)) call('connections', { id }).then((c) => setConns((m) => new Map(m).set(id, c))).catch(() => {});
  };

  const sel = selected === HOST ? null : snap.apps.find((a) => a.id === selected);
  const menuApp = menu && snap.apps.find((a) => a.id === menu.id);
  const e = snap.engine;
  const state = e.error ? 'error' : e.mode === 'demo' ? 'demo' : 'live';

  return (
    <div class="shell">
      <header class="topbar">
        <div class="brand">
          <Logo />
          <span>Bandwidth Pilot</span>
        </div>
        <nav class="tabs" role="tablist">
          {[['activity', 'Activity'], ['connections', 'Connections'], ['websites', 'Websites'], ['rules', 'Rules'], ['usage', 'Usage'], ['settings', 'Settings']].map(([id, label]) => (
            <button type="button" role="tab" aria-selected={tab === id} class={tab === id ? 'on' : ''} onClick={() => setTab(id)}>
              {label}{id === 'rules' && snap.apps.some((a) => a.hasRule) && <span class="count">{snap.apps.filter((a) => a.hasRule).length}</span>}
            </button>
          ))}
        </nav>
        <div class="masters">
          <Switch label="Blocker" checked={snap.config.blocker} onChange={(v) => onConfig({ blocker: v })} title="Apply block rules" />
          <Switch label="Limiter" checked={snap.config.limiter} onChange={(v) => onConfig({ limiter: v })} title="Apply speed limits and the cap" />
          <Switch label="Priorities" checked={snap.config.priorities} onChange={(v) => onConfig({ priorities: v })} title="Serve higher-priority apps first" />
        </div>
        <label class="search">
          <Icon name="search" size={14} />
          <input type="search" placeholder="Search apps" value={view.search}
            onInput={(ev) => { setView({ search: ev.currentTarget.value }); if (tab !== 'activity') setTab('activity'); }} />
        </label>
      </header>

      {state === 'error' && (
        <div class="banner error">
          <Icon name="info" size={16} />
          <span><b>The packet engine could not start.</b> {e.error}</span>
          <button type="button" class="btn" onClick={() => apply(call('useDemo'))}>Try the demo instead</button>
        </div>
      )}
      {state === 'demo' && (
        <div class="banner">
          <Icon name="info" size={16} />
          <span><b>Demo mode.</b> This traffic is simulated{snap.os !== 'windows' ? '; live control currently needs Windows' : ''}. Rules behave exactly as they would on real traffic.</span>
        </div>
      )}
      {error && <div class="banner error"><Icon name="info" size={16} /><span>{error}</span></div>}

      <div class="body">
        <main class="main">
          {tab === 'activity' && (
            <Activity snap={snap} view={view} setView={setView} selected={selected} setSelected={select}
              expanded={expanded} toggleExpand={toggleExpand} conns={conns} hist={hist.current} icons={icons.current}
              units={units} onRule={onRule} onMenu={setMenu} />
          )}
          {tab === 'rules' && <RulesTab snap={snap} icons={icons.current} units={units} onClear={onClear} onClearAll={onClearAll}
            onEdit={(a) => { select(a.id); setTab('activity'); }} />}
          {tab === 'usage' && <UsageTab onReset={() => call('resetUsage')} />}
          {tab === 'connections' && <ConnectionsTab search={view.search} onPickApp={(id) => { select(id); setTab('activity'); }} />}
          {tab === 'websites' && <WebsitesTab snap={snap} units={units} apply={apply} />}
          {tab === 'settings' && <SettingsTab snap={snap} units={units} setUnits={setUnits} onConfig={onConfig}
            onAsk={(v) => apply(call('setAsk', { on: v }))} />}
        </main>
        {tab === 'activity' && (
          <aside class="info" aria-label="Details">
            {sel
              ? <AppInfo key={sel.id} app={sel} snap={snap} hist={hist.current} conns={conns.get(sel.id)} icons={icons.current}
                  units={units} onRule={onRule} onClear={onClear} onReveal={onReveal} onSplit={onSplit} />
              : <HostInfo snap={snap} hist={hist.current} units={units} onConfig={onConfig} />}
          </aside>
        )}
      </div>

      <footer class="statusbar">
        <span class={'dot ' + state} />
        <span>
          {state === 'error' ? 'Engine stopped'
            : state === 'demo' ? 'Demo · simulated traffic'
              : `Live · ${e.driver} · ${e.intercepting ? 'shaping traffic' : 'watching only, no active rules'}`}
        </span>
        <span class="grow" />
        <span class="dl"><Icon name="down" size={12} />{fmtRate(snap.rateIn, units)}</span>
        <span class="ul"><Icon name="up" size={12} />{fmtRate(snap.rateOut, units)}</span>
        <span class="dim">Session {fmtBytes(snap.totalIn)} down, {fmtBytes(snap.totalOut)} up</span>
      </footer>

      <PendingCards pending={snap.pending} apply={apply} />

      {menu && menuApp && (
        <ContextMenu menu={menu} app={menuApp} live={e.mode === 'live'} onRule={onRule} onClear={onClear} onReveal={onReveal} onSplit={onSplit} onClose={() => setMenu(null)} />
      )}
    </div>
  );
}

render(<App />, document.getElementById('app'));
