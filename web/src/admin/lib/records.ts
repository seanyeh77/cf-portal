import { dayDiff, fmtDateTimeShort, relTime } from '../../shared/format';
import type { DNSRecord, Issue, Level, RecordsData, Tunnel } from '../types';

const LEVEL_RANK: Record<string, number> = { high: 0, medium: 1, low: 2, info: 3 };
export const LEVEL_NAME: Record<Level, string> = { high: '緊急', medium: '注意', low: '建議', info: '參考' };
export const PREFIX: Partial<Record<Level, string>> = { high: '緊急：', medium: '注意：' };

export const SCANNABLE = new Set(['A', 'AAAA', 'CNAME']);
/** Types the public request form offers (mirrors requestTypes in Go). */
export const REQUEST_TYPES = ['A', 'AAAA', 'CNAME', 'TXT'];
export const PROXIABLE = new Set(['A', 'AAAA', 'CNAME']);
export const EXPIRY_KINDS = new Set(['expired', 'expiring']);
/** Issues the status column leaves to other columns. */
const HIDDEN_IN_STATUS = new Set(['no_owner', 'expired', 'expiring']);


/** Issues shown in the status column, most severe first. */
export function statusIssues(r: DNSRecord): Issue[] {
  return r.issues.filter((i) => i.level !== 'info' && !HIDDEN_IN_STATUS.has(i.kind));
}

export function hasIssue(r: DNSRecord, kind: string) {
  return r.issues.some((i) => i.kind === kind);
}

/** Subdomain part of a name; '@' for the apex. */
export function sub(name: string, zone: string) {
  const n = name.toLowerCase();
  if (n === zone) return '@';
  return n.endsWith('.' + zone) ? name.slice(0, -(zone.length + 1)) : name;
}

export function normTarget(s: string) {
  return s.trim().toLowerCase().replace(/\.$/, '');
}

function isPrivateIP(s: string) {
  const v4 = s.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (v4) {
    const [a, b] = [+v4[1], +v4[2]];
    return a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254) || (a === 100 && b >= 64 && b <= 127);
  }
  const l = s.toLowerCase();
  return /^f[cd]/.test(l) || l.startsWith('fe80');
}

function isLocalIP(s: string) {
  return /^127\./.test(s) || s === '0.0.0.0' || s === '::1' || s === '::';
}

const TUNNEL_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.cfargotunnel\.com\.?$/i;

/** Warning for a Proxy setting that cannot work, or ''. */
export function proxyWarning(type: string, proxied: boolean, content: string) {
  const c = content.trim();
  if (proxied && (type === 'A' || type === 'AAAA') && isPrivateIP(c)) return '內網位址無法經 Cloudflare Proxy 連線，請取消勾選 Proxy。';
  if (!proxied && type === 'CNAME' && TUNNEL_RE.test(c)) return 'Cloudflare Tunnel 的位址只在 Cloudflare 內部有效，請勾選 Proxy。';
  return '';
}

// ---- Cloudflare Tunnel ----

const TUNNEL_STATUS: Record<string, string> = { healthy: '正常', degraded: '部分連線中斷', down: '離線', inactive: '未啟動' };

/** How a tunnel target is shown: its name, or a short id when unknown. */
export function tunnelName(t: Tunnel) {
  return t.name || t.id.slice(0, 8);
}

/** The tunnel's state in words, or '' when unknown. */
export function tunnelStatus(t: Tunnel) {
  return t.deleted ? '已刪除或不屬於此帳號' : (TUNNEL_STATUS[t.status] ?? t.status);
}

export function isPublicIP(s: string) {
  return /^[\d.]+$|:/.test(s) && !isPrivateIP(s) && !isLocalIP(s);
}

// ---- connection dot ----

export type DotState = 'ok' | 'down' | 'none' | 'pending' | 'blank';

export function dotState(r: DNSRecord, pending: boolean, now: number): { state: DotState; tip: string } {
  if (!SCANNABLE.has(r.type)) return { state: 'blank', tip: '' };
  if (pending) return { state: 'pending', tip: '設定已儲存，等待連線檢查結果' };
  if (r.meta.no_scan) return { state: 'none', tip: '已停用連線檢查' };
  const s = r.scan;
  if (!s || !s.last_ts) return { state: 'none', tip: '尚未檢查' };
  const when = `（${relTime(s.last_ts, now)}）`;
  if (s.reachable) return { state: 'ok', tip: `連線正常：${s.detail}${when}` };
  return { state: 'down', tip: `無回應：${s.detail}${when}` };
}

// ---- expiry ----

export type ExpiryView = { text: string; cls: string; tip: string; extend: boolean } | null;

/** How the expiry column shows a record. Urgency comes from the server's
 * expired/expiring issues (so muting one also calms the column). */
export function expiryView(r: DNSRecord, today: string): ExpiryView {
  const e = r.meta.expires_at;
  if (!e) return null;
  const days = dayDiff(today, e);
  const tip = `到期日 ${e}${r.meta.auto_delete ? '，到期後自動刪除' : '，到期後不會自動刪除'}`;
  const expired = hasIssue(r, 'expired');
  const expiring = hasIssue(r, 'expiring');
  const text = days < 0 ? `已過期 ${-days} 日` : days === 0 ? '今日到期' : days <= 60 ? `剩 ${days} 日` : e;
  const cls = expired || (expiring && days === 0) ? 'lv-high' : expiring ? 'lv-medium' : 'muted';
  return { text, cls, tip, extend: expired || expiring };
}

// ---- filters ----

export type Filters = {
  q: string;
  type: string;
  group: string;
  health: string;
  expiry: string;
  severity: '' | 'high' | 'medium' | 'low';
};

export const EMPTY_FILTERS: Filters = { q: '', type: '', group: '', health: '', expiry: '', severity: '' };

/** Applies every filter except severity, which callers count separately. */
export function matches(r: DNSRecord, f: Filters) {
  if (f.q) {
    const q = f.q.toLowerCase();
    const hay = [r.name, r.content, r.comment, r.meta.owner, r.meta.purpose, r.meta.notes, r.listing?.title ?? '', r.tunnel?.name ?? ''].join('\n').toLowerCase();
    if (!hay.includes(q)) return false;
  }
  if (f.type && r.type !== f.type) return false;
  if (f.group && r.group !== f.group) return false;
  if (f.health) {
    if (f.health === 'listed') {
      if (!r.listing) return false;
    } else if (f.health === 'muted') {
      if (r.muted.length === 0) return false;
    } else if (!hasIssue(r, f.health)) return false;
  }
  if (f.expiry === 'has' && !r.meta.expires_at) return false;
  if (f.expiry === 'none' && r.meta.expires_at) return false;
  if (EXPIRY_KINDS.has(f.expiry) && !hasIssue(r, f.expiry)) return false;
  return true;
}

export const EXPIRY_LABEL: Record<string, string> = { expired: '已過期', expiring: '即將到期', has: '有到期日', none: '無到期日' };

export function groupLabel(g: string) {
  return g === '_系統' ? '系統（_ 開頭）' : `${g}*`;
}

export function describeFilters(f: Filters, data: RecordsData): string[] {
  const parts: string[] = [];
  if (f.q) parts.push(`搜尋「${f.q}」`);
  if (f.type) parts.push(f.type);
  if (f.group) parts.push(groupLabel(f.group));
  if (f.health) {
    if (f.health === 'listed') parts.push('已上架到入口');
    else if (f.health === 'muted') parts.push('含已忽略項目');
    else parts.push(data.kinds[f.health]?.label ?? f.health);
  }
  if (f.expiry) parts.push(EXPIRY_LABEL[f.expiry]);
  if (f.severity) parts.push(LEVEL_NAME[f.severity]);
  return parts;
}

// ---- sorting ----

export type SortKey = 'status' | 'type' | 'name' | 'content' | 'expiry';
export type Sort = { key: SortKey; dir: 'asc' | 'desc' };

function nameKey(r: DNSRecord, zone: string) {
  const s = sub(r.name, zone).toLowerCase();
  return s === '@' ? '' : s;
}

function severityScore(r: DNSRecord) {
  const lv = r.level;
  const rank = lv ? LEVEL_RANK[lv] : 4;
  const unreachable = r.scan && r.scan.last_ts && !r.scan.reachable && !r.meta.no_scan ? 0 : 1;
  return rank * 2 + unreachable;
}

export function sortRecords(list: DNSRecord[], sort: Sort, zone: string) {
  const mul = sort.dir === 'asc' ? 1 : -1;
  const byName = (a: DNSRecord, b: DNSRecord) => nameKey(a, zone).localeCompare(nameKey(b, zone)) || a.type.localeCompare(b.type);
  const cmp: Record<SortKey, (a: DNSRecord, b: DNSRecord) => number> = {
    // "desc" puts the most severe first.
    status: (a, b) => -mul * (severityScore(a) - severityScore(b)) || byName(a, b),
    type: (a, b) => mul * a.type.localeCompare(b.type) || byName(a, b),
    name: (a, b) => mul * byName(a, b),
    content: (a, b) => mul * a.content.toLowerCase().localeCompare(b.content.toLowerCase()) || byName(a, b),
    expiry: (a, b) => {
      const ea = a.meta.expires_at || '9999-99-99';
      const eb = b.meta.expires_at || '9999-99-99';
      return mul * ea.localeCompare(eb) || byName(a, b);
    },
  };
  return [...list].sort(cmp[sort.key]);
}

// ---- naming rule (mirror of internal/dnsrec) ----

const labelRe = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;

export function nameRuleError(input: string, zone: string, rule: string): string {
  let rel = normTarget(input);
  if (rel === '' || rel === '@' || rel === zone) return '';
  if (rel.endsWith('.' + zone)) rel = rel.slice(0, -(zone.length + 1));
  let underscore = false;
  for (const seg of rel.split('.')) {
    if (seg.startsWith('_')) {
      underscore = true;
      continue;
    }
    if (seg === '*') continue;
    if (!labelRe.test(seg)) return `「${seg}」僅能使用小寫英文、數字與連字號，且不可以連字號開頭或結尾`;
  }
  if (rule && !underscore) {
    try {
      if (!new RegExp(`^(?:${rule})$`).test(rel)) return `「${rel}」不符合自訂命名規則`;
    } catch {
      /* invalid rule passes */
    }
  }
  return '';
}

export function fqdn(input: string, zone: string) {
  const n = normTarget(input);
  if (n === '' || n === '@' || n === zone) return zone;
  return n.endsWith('.' + zone) ? n : `${n}.${zone}`;
}

export function syncedTip(ts: number) {
  return `最後自 Cloudflare 同步：${fmtDateTimeShort(ts)}`;
}

export const TTL_OPTIONS: [number, string][] = [
  [1, 'Auto'],
  [60, '1 min'],
  [300, '5 min'],
  [1800, '30 min'],
  [3600, '1 hr'],
  [43200, '12 hr'],
  [86400, '1 day'],
];

export function ttlLabel(ttl: number) {
  const o = TTL_OPTIONS.find(([v]) => v === ttl);
  return o ? o[1] : `${ttl} 秒`;
}

/** CNAMEs in the zone that point at name. */
export function dependents(records: DNSRecord[], name: string) {
  const n = normTarget(name);
  return records.filter((r) => r.type === 'CNAME' && normTarget(r.content) === n);
}

/** How many CNAMEs point at each name, for dnsRisks. */
export function cnameTargetCounts(records: DNSRecord[]) {
  const m = new Map<string, number>();
  for (const r of records) if (r.type === 'CNAME') m.set(normTarget(r.content), (m.get(normTarget(r.content)) ?? 0) + 1);
  return m;
}

/** Why changing record to after deserves a closer look; empty when it is routine. */
export function dnsRisks(record: DNSRecord, after: { type: string; name: string; content: string; proxied: boolean }, cnameCounts: Map<string, number>) {
  const reasons: string[] = [];
  const nameChanged = normTarget(record.name) !== normTarget(after.name);
  const contentChanged = normTarget(record.content) !== normTarget(after.content);
  if (record.proxied && !after.proxied && ['A', 'AAAA'].includes(after.type) && isPublicIP(after.content)) reasons.push('停用 Proxy 後，來源主機的 IP 會公開。');
  if (record.type !== after.type || nameChanged) reasons.push('變更類型或名稱等同刪除舊 record 再新增，舊名稱將立即失效。');
  const deps = cnameCounts.get(normTarget(record.name)) ?? 0;
  if (deps && (nameChanged || contentChanged)) reasons.push(`另有 ${deps} 筆 CNAME 指向此 record，會一併受影響。`);
  if (record.listing) reasons.push('此 record 已上架到服務入口。');
  return reasons;
}

/** The records with the given ids, in that order, skipping unknown ids. */
export function pickRecords(data: RecordsData, ids: string[]) {
  const byID = new Map(data.records.map((r) => [r.id, r]));
  return ids.map((id) => byID.get(id)).filter((r): r is DNSRecord => !!r);
}

/** Splits a tag list on half- or full-width commas and 、, like the server. */
export const splitTags = (s: string) => [
  ...new Set(
    s
      .split(/[,，、]/)
      .map((t) => t.trim())
      .filter(Boolean),
  ),
];

/** The title a new portal listing starts with: a short purpose, else the subdomain. */
export function defaultListingTitle(r: DNSRecord, zone: string) {
  return (r.meta.purpose && [...r.meta.purpose].length <= 20 ? r.meta.purpose : sub(r.name, zone)) || r.name;
}

/** Labels of DNS record fields, keyed by API name. */
export const DNS_FIELD_LABEL: Record<string, string> = {
  type: '類型',
  name: '名稱',
  content: '指向',
  ttl: 'TTL',
  proxied: 'Proxy',
  priority: 'Priority',
  comment: '備註',
};

/** A DNS field value as shown to people. */
export function fieldValue(field: string, v: unknown) {
  if (field === 'proxied') return v ? '啟用' : '停用';
  if (field === 'ttl' && typeof v === 'number') return ttlLabel(v);
  if (v == null || v === '') return '（空）';
  return String(v);
}

/** Distinct non-empty values of pick, sorted. */
export function distinctOf(records: DNSRecord[], pick: (r: DNSRecord) => string | undefined) {
  return Array.from(new Set(records.map(pick).filter((v): v is string => !!v))).sort();
}

/** Distinct owners, for owner field suggestions. */
export const ownersOf = (records: DNSRecord[]) => distinctOf(records, (r) => r.meta.owner);

/** Distinct portal categories, for category field suggestions. */
export const categoriesOf = (records: DNSRecord[]) => distinctOf(records, (r) => r.listing?.category);
