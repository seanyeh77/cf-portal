export type Level = 'high' | 'medium' | 'low' | 'info';

export type Issue = {
  kind: string;
  level: Level;
  label: string;
  help: string;
  detail: string;
  upstream?: string;
};

export type Meta = {
  owner: string;
  purpose: string;
  notes: string;
  expires_at: string;
  auto_delete: boolean;
  no_scan: boolean;
};

export type Scan = {
  last_ts: number;
  last_ok_ts: number | null;
  reachable: number;
  detail: string;
  dns_detail: string;
};

export type Listing = {
  record_id: string;
  hostname: string;
  title: string;
  description: string;
  category: string;
  url: string | null;
  sort_order: number;
  featured: boolean;
  tags: string;
  icon: string;
  icon_file: string | null;
  created_at: number;
  effective_url: string;
  icon_url: string;
};

export type DNSRecord = RecordInput & {
  id: string;
  created_on?: string;
  modified_on?: string;
  editable: boolean;
  group: string;
  /** Row level from the server: the most severe non-info issue, or ''. */
  level: Level | '';
  /** Canonical target for A/AAAA/CNAME, shared by records that point alike. */
  target_key: string;
  listing: Listing | null;
  meta: Meta;
  scan: Scan | null;
  issues: Issue[];
  muted: Issue[];
};

export type Kind = { level: Level; label: string; help: string };

export type ScanStatus = {
  running: boolean;
  last_ts: number;
  count: number;
  error: string;
  dns_ok: boolean;
  interval: number;
  /** Records whose single-record check is in progress. */
  pending: string[];
};

export type RecordsData = {
  zone: string;
  today: string;
  synced_at: number;
  now: number;
  records: DNSRecord[];
  orphans: { record_id: string; hostname: string; title: string }[];
  summary: Record<string, number>;
  kinds: Record<string, Kind>;
  kind_order: string[];
  groups: [string, number][];
  scan: ScanStatus;
  pending_requests: number;
  last_snapshot: number;
  settings: {
    zombie_days: string;
    expiry_warn_days: string;
    scan_ports: string;
    name_rule_hint: string;
    name_rule: string;
    requests_enabled: string;
  };
};

export type RecordInput = {
  type: string;
  name: string;
  content: string;
  ttl: number;
  proxied: boolean;
  priority: number | null;
  comment: string;
};

export type AuditEntry = {
  id: number;
  ts: number;
  ip: string;
  action: string;
  target: string;
  detail: string;
};

export type RequestItem = {
  id: number;
  ts: number;
  ip: string;
  requester: string;
  contact: string;
  name: string;
  type: string;
  content: string;
  purpose: string;
  expires_at: string;
  status: 'pending' | 'approved' | 'rejected';
  admin_note: string;
  decided_ts: number | null;
  record_id: string | null;
  conflict: string;
  name_ok: boolean;
  name_error: string;
};

export type SnapshotInfo = { name: string; ts: number; source: 'auto' | 'manual'; count: number; size: number };

export type SnapRecord = RecordInput & { id: string; meta: Meta };

export type SnapshotDiff = {
  name: string;
  ts: number;
  source: string;
  diff: {
    removed: SnapRecord[];
    changed: { fields: string[]; then: SnapRecord; now: DNSRecord }[];
    added: DNSRecord[];
  };
};

export type Settings = Record<string, string> & { logo_url: string; hero_url: string; zone: string };
