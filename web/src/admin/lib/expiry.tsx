import { addMonths, nextCycleEnd, yearEnd } from '../../shared/format';
import type { RecordsData } from '../types';

/** A quick expiry choice: a button label, the "延長…" wording and the date. */
export type ExpiryPreset = { label: string; extend: string; date: string };

/** The custom expiry cycle from the settings: its name and MM-DD end dates. */
export type ExpiryCycle = { name: string; dates: string };

/** Expiry choices counted from base: one and three months, the year end and the custom cycle. */
export function expiryPresets(base: string, cycle: ExpiryCycle): ExpiryPreset[] {
  const out: ExpiryPreset[] = [
    { label: '一個月', extend: '延長一個月', date: addMonths(base, 1) },
    { label: '三個月', extend: '延長三個月', date: addMonths(base, 3) },
    { label: '年底', extend: '延長到年底', date: yearEnd(base) },
  ];
  const end = nextCycleEnd(
    base,
    cycle.dates
      .split(',')
      .map((d) => d.trim())
      .filter(Boolean),
  );
  if (cycle.name && end) out.push({ label: cycle.name, extend: '延長到' + cycle.name, date: end });
  return out;
}

/** Buttons that fill an expiry date field with the presets counted from today. */
export function ExpiryButtons({ data, disabled, onPick }: { data: RecordsData; disabled?: boolean; onPick: (date: string) => void }) {
  return (
    <>
      {expiryPresets(data.today, { name: data.settings.expiry_cycle_name, dates: data.settings.expiry_cycle_dates }).map((p) => (
        <button key={p.label} type="button" className="btn small" disabled={disabled} data-tip={p.date} onClick={() => onPick(p.date)}>
          {p.label}
        </button>
      ))}
    </>
  );
}
