const dtfShort = new Intl.DateTimeFormat('zh-TW', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hourCycle: 'h23',
});
const hm = new Intl.DateTimeFormat('zh-TW', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });

/** 2026/10/22 21:54 */
export function fmtDateTimeShort(ts: number) {
  return dtfShort.format(new Date(ts * 1000));
}
/** 14:30 */
export function fmtHM(ts: number) {
  return hm.format(new Date(ts * 1000));
}

/** 剛剛、x 分鐘前、x 小時前、x 天前 */
export function relTime(ts: number, now = Date.now() / 1000) {
  const d = Math.max(0, now - ts);
  if (d < 60) return '剛剛';
  if (d < 3600) return `${Math.floor(d / 60)} 分鐘前`;
  if (d < 86400) return `${Math.floor(d / 3600)} 小時前`;
  return `${Math.floor(d / 86400)} 天前`;
}

/** Days between two YYYY-MM-DD dates (b - a). */
export function dayDiff(a: string, b: string) {
  const pa = Date.UTC(+a.slice(0, 4), +a.slice(5, 7) - 1, +a.slice(8, 10));
  const pb = Date.UTC(+b.slice(0, 4), +b.slice(5, 7) - 1, +b.slice(8, 10));
  return Math.round((pb - pa) / 86400000);
}

function iso(d: Date) {
  return d.toISOString().slice(0, 10);
}

/** Adds n calendar months, clamping to the month's last day. */
export function addMonths(day: string, n: number) {
  const y = +day.slice(0, 4);
  const m = +day.slice(5, 7) - 1 + n;
  const d = +day.slice(8, 10);
  const last = new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
  return iso(new Date(Date.UTC(y, m, Math.min(d, last))));
}

/** December 31 of the day's year. */
export function yearEnd(day: string) {
  return day.slice(0, 4) + '-12-31';
}

/** The first of the yearly MM-DD dates on or after day, or '' when there are none. */
export function nextCycleEnd(day: string, dates: string[]) {
  const md = day.slice(5);
  const sorted = [...dates].sort();
  if (!sorted.length) return '';
  const next = sorted.find((d) => d >= md);
  return next ? `${day.slice(0, 4)}-${next}` : `${+day.slice(0, 4) + 1}-${sorted[0]}`;
}

export function pct(v: number | null | undefined) {
  if (v == null) return '—';
  const p = v * 100;
  return (p >= 99.95 ? '100' : p.toFixed(1)) + '%';
}
