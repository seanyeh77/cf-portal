import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { fmtDateTimeShort } from '../../shared/format';
import { confirmDialog, Tip } from '../../shared/ui';
import { act } from '../lib/data';
import { DNS_FIELD_LABEL, fieldValue } from '../lib/records';
import type { SnapshotDiff, SnapshotInfo, SnapRecord } from '../types';

const field = (r: object, f: string) => (r as Record<string, unknown>)[f];

export function Snapshots() {
  const q = useQuery({ queryKey: ['snapshots'], queryFn: () => api<{ snapshots: SnapshotInfo[]; keep: number }>('GET', '/api/snapshots') });
  // The comparison is a query, so every mutation's refresh updates it.
  const [compared, setCompared] = useState('');
  const diffQ = useQuery({
    queryKey: ['snapshot-diff', compared],
    queryFn: () => api<SnapshotDiff>('GET', `/api/snapshots/${encodeURIComponent(compared)}/diff`),
    enabled: compared !== '',
  });
  const diff = diffQ.data ?? null;
  const [busy, setBusy] = useState(false);

  const create = async () => {
    setBusy(true);
    await act(() => api('POST', '/api/snapshots'), '已建立快照');
    setBusy(false);
  };
  const compare = (name: string) => (name === compared ? diffQ.refetch() : setCompared(name));
  const restore = async (title: string, body: Record<string, unknown>, describe: string, danger = false) => {
    const ok = await confirmDialog({ title, body: <p className="pre">{describe}</p>, confirmText: title, danger });
    if (!ok) return;
    await act(() => api('POST', '/api/records/restore', body), '已還原');
  };
  const input = (r: SnapRecord) => ({ type: r.type, name: r.name, content: r.content, ttl: r.ttl, proxied: r.proxied, priority: r.priority, comment: r.comment });

  return (
    <div className="snapshots">
      <div className="page-head">
        <h1>
          快照 <Tip text={`每日自動保存（保留 ${q.data?.keep ?? 30} 份），批次刪除前亦會保存。可與目前比較並逐筆還原。`} />
        </h1>
        <button type="button" className="btn primary" disabled={busy} onClick={create}>
          立即建立快照
        </button>
      </div>
      {(q.error || diffQ.error) && <div className="errbox">{errMsg(q.error ?? diffQ.error)}</div>}
      <table className="plain snap-table">
        <thead>
          <tr>
            <th>時間</th>
            <th>來源</th>
            <th>筆數</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {q.data?.snapshots.length === 0 && (
            <tr>
              <td colSpan={4} className="muted">
                尚無快照
              </td>
            </tr>
          )}
          {q.data?.snapshots.map((s) => (
            <tr key={s.name} className={diff?.name === s.name ? 'selected' : undefined}>
              <td>{fmtDateTimeShort(s.ts)}</td>
              <td>{s.source === 'auto' ? '自動' : '手動'}</td>
              <td>{s.count}</td>
              <td className="row-gap">
                <button type="button" className="btn small" onClick={() => compare(s.name)}>
                  比較
                </button>
                <a className="btn small" href={`/api/snapshots/${s.name}/download?format=json`}>
                  JSON
                </a>
                <a className="btn small" href={`/api/snapshots/${s.name}/download?format=zone`}>
                  Zone 檔
                </a>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {diff && (
        <section className="diff-result" data-testid="snapshot-diff">
          <h2 className="sec-title">與 {fmtDateTimeShort(diff.ts)} 比較</h2>
          <h3 className="diff-head">
            已刪除（{diff.diff.removed.length}） <Tip text="快照中有、目前已不存在的 record。可重新建立，並一併還原當時的管理資訊。" />
          </h3>
          {diff.diff.removed.length === 0 && <p className="muted small">無</p>}
          {diff.diff.removed.map((r) => (
            <div key={r.id} className="diff-row">
              <span className="mono">
                {r.type} {r.name} → {r.content}
              </span>
              {r.meta?.owner && <span className="small muted">負責人：{r.meta.owner}</span>}
              <span className="spacer" />
              <button
                type="button"
                className="btn small"
                onClick={() => restore('重新建立', { mode: 'recreate', record_id: r.id, data: input(r), meta: r.meta }, `重新建立 ${r.type} ${r.name} → ${r.content}`)}
              >
                重新建立
              </button>
            </div>
          ))}
          <h3 className="diff-head">
            已修改（{diff.diff.changed.length}） <Tip text="刪除線為快照時的值，其後為目前的值。「改回」會將 DNS 設定恢復為快照時的狀態。" />
          </h3>
          {diff.diff.changed.length === 0 && <p className="muted small">無</p>}
          {diff.diff.changed.map((c) => (
            <div key={c.now.id} className="diff-row">
              <span className="mono">
                {c.now.type} {c.now.name}
              </span>
              <span className="small">
                {c.fields.map((f) => (
                  <span key={f} className="diff-field">
                    {DNS_FIELD_LABEL[f] ?? f}：<del className="old">{fieldValue(f, field(c.then, f))}</del> → {fieldValue(f, field(c.now, f))}
                  </span>
                ))}
              </span>
              <span className="spacer" />
              <button
                type="button"
                className="btn small"
                onClick={() =>
                  restore(
                    '改回',
                    { mode: 'revert', record_id: c.now.id, data: input(c.then) },
                    `將 ${c.now.name} 改回快照時的設定：\n${c.fields.map((f) => `${DNS_FIELD_LABEL[f] ?? f}：${fieldValue(f, field(c.now, f))} → ${fieldValue(f, field(c.then, f))}`).join('\n')}`,
                  )
                }
              >
                改回
              </button>
            </div>
          ))}
          <h3 className="diff-head">
            新增（{diff.diff.added.length}） <Tip text="快照之後才建立的 record。「刪除」會自 Cloudflare 移除，可於操作紀錄還原。" />
          </h3>
          {diff.diff.added.length === 0 && <p className="muted small">無</p>}
          {diff.diff.added.map((r) => (
            <div key={r.id} className="diff-row">
              <span className="mono">
                {r.type} {r.name} → {r.content}
              </span>
              <span className="spacer" />
              <button
                type="button"
                className="btn small danger-text"
                onClick={() => restore('刪除', { mode: 'delete', record_id: r.id }, `自 Cloudflare 刪除 ${r.type} ${r.name} → ${r.content}`, true)}
              >
                刪除
              </button>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}
