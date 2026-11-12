import { useEffect, useState } from 'preact/hooks';
import { KB, MB, GB } from './format.js';

export function Switch({ checked, onChange, label, disabled, title }) {
  return (
    <label class={'switch' + (disabled ? ' disabled' : '')} title={title}>
      <input type="checkbox" checked={!!checked} disabled={disabled}
        onChange={(e) => onChange(e.currentTarget.checked)} />
      <span class="track"><span class="thumb" /></span>
      {label && <span class="switch-label">{label}</span>}
    </label>
  );
}

export function Segmented({ options, value, onChange, small }) {
  return (
    <div class={'segmented' + (small ? ' small' : '')} role="radiogroup">
      {options.map((o) => (
        <button type="button" role="radio" aria-checked={o.value === value}
          class={o.value === value ? 'on' : ''} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

// A number box with a unit picker. value is in base units (bytes or bytes/s);
// onCommit fires when the user finishes editing.
function AmountField({ value, onCommit, units, disabled, label }) {
  const pick = (v) => {
    for (let i = units.length - 1; i > 0; i--) if (v >= units[i].size && v % units[i].size === 0) return i;
    for (let i = units.length - 1; i > 0; i--) if (v >= units[i].size * 10) return i;
    return 0;
  };
  const [unit, setUnit] = useState(() => pick(value));
  const [text, setText] = useState('');
  const [editing, setEditing] = useState(false);
  const shown = () => String(+(value / units[unit].size).toFixed(2));
  useEffect(() => { if (!editing) setText(shown()); }, [value, unit, editing]);

  const commit = (t, u) => {
    const n = parseFloat(String(t).replace(',', '.'));
    if (isFinite(n) && n > 0) onCommit(Math.round(n * units[u].size));
    else setText(shown());
  };
  return (
    <span class={'amount' + (disabled ? ' disabled' : '')}>
      <input type="text" inputMode="decimal" aria-label={label} value={text} disabled={disabled}
        onFocus={(e) => { setEditing(true); e.currentTarget.select(); }}
        onInput={(e) => setText(e.currentTarget.value)}
        onBlur={() => { setEditing(false); commit(text, unit); }}
        onKeyDown={(e) => { if (e.key === 'Enter') e.currentTarget.blur(); }} />
      <select aria-label={label + ' unit'} value={unit} disabled={disabled}
        onChange={(e) => { const u = +e.currentTarget.value; setUnit(u); commit(text, u); }}>
        {units.map((u, i) => <option value={i}>{u.label}</option>)}
      </select>
    </span>
  );
}

const RATE_UNITS = [{ label: 'KB/s', size: KB }, { label: 'MB/s', size: MB }];
const SIZE_UNITS = [{ label: 'MB', size: MB }, { label: 'GB', size: GB }];

export const RateField = (p) => <AmountField {...p} units={RATE_UNITS} />;
export const SizeField = (p) => <AmountField {...p} units={SIZE_UNITS} />;
