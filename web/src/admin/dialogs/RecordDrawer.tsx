import { useQuery } from '@tanstack/react-query';
import { api } from '../../shared/api';
import { fmtDateTimeShort, relTime } from '../../shared/format';
import { confirmDialog, Dialog, Tip } from '../../shared/ui';
import { act } from '../lib/data';
import { ACTION_LABEL, restorePlan, summarize } from '../lib/audit';
import { dependents, LEVEL_NAME, SCANNABLE, statusIssues, sub, ttlLabel, tunnelStatus } from '../lib/records';
import { drawerStore, modalStore, openDrawer, openEditor } from '../lib/store';
import type { AuditEntry, DNSRecord, RecordsData } from '../types';

const mute = (r: DNSRecord, kind: string, muted: boolean) =>
  act(() => api('POST', '/api/issues/mute', { record_id: r.id, kind, muted }), muted ? '已忽略' : '已還原');

export async function runRestore(e: AuditEntry) {
  const plan = restorePlan(e);
  if (!plan) return;
  const ok = await confirmDialog({
    title: `${plan.label}：${e.target}`,
    body: <p className="pre">{plan.describe}</p>,
    confirmText: plan.label,
    danger: plan.danger,
  });
  if (!ok) return;
  await act(() => api('POST', '/api/records/restore', plan.body), '已還原');
}

function History({ r }: { r: DNSRecord }) {
  const q = useQuery({
    queryKey: ['history', r.id, r.modified_on, r.meta],
    queryFn: () => api<{ entries: AuditEntry[] }>('GET', `/api/records/${r.id}/history?name=${encodeURIComponent(r.name)}`),
  });
  if (q.isLoading) return <p className="muted small">讀取中…</p>;
  const rows = q.data?.entries ?? [];
  if (!rows.length) return <p className="muted small">尚無紀錄</p>;
  return (
    <table className="plain small">
      <tbody>
        {rows.map((e) => {
          const plan = restorePlan(e);
          return (
            <tr key={e.id}>
              <td className="nowrap muted">{fmtDateTimeShort(e.ts)}</td>
              <td className="nowrap">{ACTION_LABEL[e.action] ?? e.action}</td>
              <td className="wrap-any">{summarize(e)}</td>
              <td>
                {plan && (
                  <button type="button" className="linkbtn" onClick={() => runRestore(e)}>
                    還原
                  </button>
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function Def({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <>
      <dt>{k}</dt>
      <dd>{children}</dd>
    </>
  );
}

export function RecordDrawer({ r, data }: { r: DNSRecord; data: RecordsData }) {
  const zone = data.zone;
  const close = () => drawerStore.set(null);
  const deps = dependents(data.records, r.name).filter((d) => d.id !== r.id);
  const now = Date.now() / 1000;
  const recheck = () => act(() => api('POST', `/api/records/${r.id}/scan`), '已開始連線檢查');
  return (
    <Dialog
      className="drawer"
      dismissable
      onClose={close}
      testId="record-drawer"
      title={
        <span className="drawer-title">
          <span className="small muted">
            {r.type}
            {r.group && ` · ${r.group}`}
          </span>
          <span className="mono">{r.name}</span>
        </span>
      }
    >
      <div className="drawer-actions">
        <button
          type="button"
          className="btn primary"
          disabled={!r.editable}
          data-tip={r.editable ? undefined : `${r.type} 請於 Cloudflare 編輯`}
          onClick={() => openEditor(r.id)}
        >
          編輯
        </button>
        <button type="button" className="btn" onClick={() => modalStore.set({ kind: 'listing', id: r.id })}>
          {r.listing ? '入口設定' : '上架'}
        </button>
        {SCANNABLE.has(r.type) && (
          <button type="button" className="btn" onClick={recheck}>
            重新檢查
          </button>
        )}
        <a className="btn" href={r.listing?.effective_url ?? `https://${r.name}`} target="_blank" rel="noreferrer">
          開啟 ↗
        </a>
        <span className="spacer" />
        <button type="button" className="btn danger-text" onClick={() => modalStore.set({ kind: 'delete', id: r.id })}>
          刪除
        </button>
      </div>

      <section className="dsec">
        <h3>待處理</h3>
        {r.issues.length === 0 && <p className="muted small">無待處理項目</p>}
        {r.issues.map((i) => (
          <div key={i.kind} className={'issue-block lvb-' + i.level}>
            <div className="issue-head">
              <strong>{i.label}</strong>
              <span className={'small lv-' + i.level}>{LEVEL_NAME[i.level]}</span>
              <Tip text={i.help} />
              <span className="spacer" />
              {i.upstream && (
                <button type="button" className="linkbtn" onClick={() => openDrawer(i.upstream!)}>
                  前往上游
                </button>
              )}
              <button type="button" className="linkbtn" data-tip="不再列為待處理" onClick={() => mute(r, i.kind, true)}>
                忽略
              </button>
            </div>
            {i.detail && <div className="small muted">{i.detail}</div>}
          </div>
        ))}
        {r.muted.map((i) => (
          <div key={i.kind} className="small muted muted-line">
            已忽略：{i.label}（
            <button type="button" className="linkbtn" onClick={() => mute(r, i.kind, false)}>
              還原
            </button>
            ）
          </div>
        ))}
      </section>

      <section className="dsec">
        <h3>
          管理資訊
          <button type="button" className="linkbtn small" disabled={!r.editable} onClick={() => openEditor(r.id, 'owner')}>
            編輯
          </button>
        </h3>
        <dl className="defs">
          <Def k="負責人">{r.meta.owner || <span className="muted">未指派</span>}</Def>
          <Def k="用途">{r.meta.purpose || <span className="muted">未填寫</span>}</Def>
          <Def k="備註">{r.meta.notes ? <span className="pre">{r.meta.notes}</span> : <span className="muted">無</span>}</Def>
          <Def k="到期日">{r.meta.expires_at ? `${r.meta.expires_at}${r.meta.auto_delete ? '（自動刪除）' : ''}` : '無'}</Def>
          <Def k="連線檢查">{r.meta.no_scan ? '停用' : '啟用'}</Def>
        </dl>
      </section>

      <section className="dsec">
        <h3>DNS 設定</h3>
        <dl className="defs">
          <Def k="Type">{r.type}</Def>
          <Def k="指向">
            <span className="mono wrap-any">{r.content}</span>
          </Def>
          {r.tunnel && (
            <Def k="Tunnel">
              {r.tunnel.name ? <span className="mono">{r.tunnel.name}</span> : <span className="muted">名稱未知</span>}
              {tunnelStatus(r.tunnel) && `（${tunnelStatus(r.tunnel)}）`}
            </Def>
          )}
          {r.priority != null && <Def k="Priority">{r.priority}</Def>}
          <Def k="Proxy">{r.proxied ? '啟用' : '停用'}</Def>
          <Def k="TTL">{ttlLabel(r.ttl)}</Def>
          <Def k="備註">{r.comment || <span className="muted">無</span>}</Def>
          <Def k="建立時間">{r.created_on ? fmtDateTimeShort(Date.parse(r.created_on) / 1000) : '—'}</Def>
          <Def k="最後修改">{r.modified_on ? fmtDateTimeShort(Date.parse(r.modified_on) / 1000) : '—'}</Def>
        </dl>
      </section>

      {SCANNABLE.has(r.type) && (
        <section className="dsec">
          <h3>連線檢查</h3>
          {r.meta.no_scan ? (
            <p className="muted small">已停用連線檢查</p>
          ) : !r.scan ? (
            <p className="muted small">尚未檢查</p>
          ) : (
            <dl className="defs">
              <Def k="結果">{r.scan.reachable ? <span className="ok-text">正常</span> : '無回應'}</Def>
              <Def k="詳細">{r.scan.detail || '—'}</Def>
              {r.type === 'CNAME' && <Def k="CNAME 目標">{r.scan.dns_detail || '—'}</Def>}
              <Def k="檢查時間">
                {fmtDateTimeShort(r.scan.last_ts)}（{relTime(r.scan.last_ts, now)}）
              </Def>
              <Def k="最後回應">{r.scan.last_ok_ts ? fmtDateTimeShort(r.scan.last_ok_ts) : '從未回應'}</Def>
            </dl>
          )}
        </section>
      )}

      {deps.length > 0 && (
        <section className="dsec">
          <h3>
            影響範圍 <Tip text="以下 CNAME 指向此 record，修改或刪除會一併影響" />
          </h3>
          <table className="plain small">
            <thead>
              <tr>
                <th>名稱</th>
                <th>負責人</th>
                <th>其他問題</th>
              </tr>
            </thead>
            <tbody>
              {deps.map((d) => {
                const top = statusIssues(d).find((i) => i.kind !== 'upstream_broken') ?? d.issues.find((i) => i.kind === 'no_owner');
                return (
                  <tr key={d.id}>
                    <td>
                      <button type="button" className="linkbtn mono" onClick={() => openDrawer(d.id)}>
                        {sub(d.name, zone)}
                      </button>
                    </td>
                    <td>{d.meta.owner || <span className="muted">未指派</span>}</td>
                    <td className={top ? 'lv-' + top.level : 'muted'}>{top ? top.label : '無'}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      )}

      <section className="dsec">
        <h3>服務入口</h3>
        {r.listing ? (
          <dl className="defs">
            <Def k="顯示名稱">{r.listing.title}</Def>
            <Def k="分類">{r.listing.category || '其他'}</Def>
            <Def k="網址">
              <a href={r.listing.effective_url} target="_blank" rel="noreferrer" className="wrap-any">
                {r.listing.effective_url}
              </a>
            </Def>
          </dl>
        ) : (
          <p className="muted small">未上架</p>
        )}
      </section>

      <section className="dsec">
        <h3>變更紀錄</h3>
        <History r={r} />
      </section>
    </Dialog>
  );
}
