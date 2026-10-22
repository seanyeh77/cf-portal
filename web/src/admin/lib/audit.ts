import type { AuditEntry, Meta, RecordInput } from '../types';

export const ACTION_LABEL: Record<string, string> = {
  login: '登入',
  login_failed: '登入失敗',
  record_create: '新增 record',
  record_update: '修改 record',
  record_delete: '刪除 record',
  record_auto_delete: '到期自動刪除',
  record_restore: '還原 record',
  record_restore_delete: '還原（刪除）',
  meta_update: '修改管理資訊',
  bulk_meta: '批次修改管理資訊',
  listing_save: '上架或修改入口',
  listing_remove: '從入口下架',
  icon_upload: '上傳服務圖示',
  settings_save: '修改網站設定',
  media_upload: '上傳網站圖片',
  media_remove: '移除網站圖片',
  issue_mute: '忽略項目',
  issue_unmute: '還原忽略項目',
  snapshot_create: '建立快照',
  request_approve: '核准申請',
  request_reject: '駁回申請',
};

export type Detail = {
  record_id?: string;
  before?: RecordInput;
  after?: RecordInput;
  meta?: Meta;
  meta_before?: Meta;
  meta_after?: Meta;
  [k: string]: unknown;
};

export function parseDetail(e: AuditEntry): Detail {
  try {
    return JSON.parse(e.detail || '{}');
  } catch {
    return {};
  }
}

const q = (v: unknown) => (v === '' || v == null ? '""' : JSON.stringify(v));

const DNS_FIELDS: (keyof RecordInput)[] = ['type', 'name', 'content', 'ttl', 'proxied', 'priority', 'comment'];
const META_FIELDS: (keyof Meta)[] = ['owner', 'purpose', 'notes', 'expires_at', 'auto_delete', 'no_scan'];

export function fieldChanges<T extends object>(a: T | undefined, b: T | undefined, fields: (keyof T)[]) {
  const out: string[] = [];
  if (!a || !b) return out;
  for (const f of fields) {
    const va = (a as Record<string, unknown>)[f as string] ?? '';
    const vb = (b as Record<string, unknown>)[f as string] ?? '';
    if (JSON.stringify(va) !== JSON.stringify(vb)) out.push(`${String(f)}: ${q(va)} → ${q(vb)}`);
  }
  return out;
}

function short(r?: RecordInput) {
  if (!r) return '';
  return `${r.type} ${r.name} → ${r.content}`;
}

/** One-line summary of an audit entry. */
export function summarize(e: AuditEntry): string {
  const d = parseDetail(e);
  switch (e.action) {
    case 'record_update':
    case 'record_restore': {
      const parts = [...fieldChanges(d.before, d.after, DNS_FIELDS), ...fieldChanges(d.meta_before, d.meta_after, META_FIELDS)];
      if (!d.before && d.after) return `重新建立 ${short(d.after)}`;
      return parts.join('；') || '無變更';
    }
    case 'record_create':
    case 'request_approve':
      return short(d.after);
    case 'record_delete':
    case 'record_auto_delete':
    case 'record_restore_delete':
      return short(d.before);
    case 'meta_update':
      return fieldChanges(d.meta_before, d.meta_after, META_FIELDS).join('；');
    case 'bulk_meta': {
      const set = (d.set ?? {}) as Record<string, unknown>;
      return Object.entries(set)
        .map(([k, v]) => `${k} = ${q(v)}`)
        .join('；');
    }
    case 'issue_mute':
    case 'issue_unmute':
      return String(d.label ?? d.kind ?? '');
    case 'listing_save':
    case 'listing_remove':
      return String(d.title ?? '');
    case 'settings_save': {
      const ch = (d.changes ?? {}) as Record<string, [string, string]>;
      return Object.entries(ch)
        .map(([k, [a, b]]) => `${k}: ${q(a)} → ${q(b)}`)
        .join('；');
    }
    case 'request_reject':
      return `原因：${String(d.note ?? '')}`;
    case 'snapshot_create':
      return d.reason === 'bulk_delete' ? `批次刪除前自動保存（${d.count} 筆）` : `${d.count ?? ''} 筆`;
    default:
      return '';
  }
}

export type RestorePlan = {
  label: string;
  describe: string;
  body: Record<string, unknown>;
  danger?: boolean;
};

/** How an audit entry can be undone, or null. */
export function restorePlan(e: AuditEntry): RestorePlan | null {
  const d = parseDetail(e);
  const id = d.record_id;
  switch (e.action) {
    case 'record_update':
      if (!id || !d.before) return null;
      return {
        label: '改回',
        describe: `將 ${e.target} 改回修改前的設定：\n${[...fieldChanges(d.after, d.before, DNS_FIELDS), ...fieldChanges(d.meta_after, d.meta_before, META_FIELDS)].join('\n')}`,
        body: { mode: 'revert', record_id: id, data: d.before, meta: d.meta_before },
      };
    case 'record_restore':
      if (!id) return null;
      if (d.before)
        return {
          label: '復原',
          describe: `將 ${e.target} 復原為還原前的設定：\n${fieldChanges(d.after, d.before, DNS_FIELDS).join('\n')}`,
          body: { mode: 'revert', record_id: id, data: d.before, meta: d.meta_before },
        };
      return { label: '刪除', describe: `自 Cloudflare 刪除 ${short(d.after)}`, body: { mode: 'delete', record_id: id }, danger: true };
    case 'record_delete':
    case 'record_auto_delete':
    case 'record_restore_delete':
      if (!d.before) return null;
      return {
        label: '重新建立',
        describe: `重新建立 ${short(d.before)}${d.meta?.owner ? `\n負責人：${d.meta.owner}` : ''}`,
        body: { mode: 'recreate', record_id: id, data: d.before, meta: d.meta },
      };
    case 'record_create':
    case 'request_approve':
      if (!id) return null;
      return { label: '刪除', describe: `自 Cloudflare 刪除 ${short(d.after) || e.target}`, body: { mode: 'delete', record_id: id }, danger: true };
    case 'meta_update':
      if (!id || !d.meta_before) return null;
      return {
        label: '改回',
        describe: `將 ${e.target} 的管理資訊改回：\n${fieldChanges(d.meta_after, d.meta_before, META_FIELDS).join('\n')}`,
        body: { mode: 'meta', record_id: id, meta: d.meta_before },
      };
  }
  return null;
}
