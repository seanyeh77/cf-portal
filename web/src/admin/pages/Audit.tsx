import { useQuery } from '@tanstack/react-query';
import { api, errMsg } from '../../shared/api';
import { fmtDateTimeShort } from '../../shared/format';
import { ACTION_LABEL, restorePlan, summarize } from '../lib/audit';
import { runRestore } from '../dialogs/RecordDrawer';
import type { AuditEntry } from '../types';

export function AuditPage() {
  const q = useQuery({ queryKey: ['audit'], queryFn: () => api<{ entries: AuditEntry[] }>('GET', '/api/audit') });
  if (q.error) return <div className="errbox">{errMsg(q.error)}</div>;
  if (!q.data) return <p className="muted">讀取中…</p>;
  return (
    <div className="audit">
      <div className="page-head">
        <h1>最近 200 筆操作</h1>
      </div>
      <div className="table-wrap">
        <table className="plain audit-table">
          <thead>
            <tr>
              <th>時間</th>
              <th>來源 IP</th>
              <th>操作</th>
              <th>對象</th>
              <th>內容摘要</th>
              <th>
                <span className="sr-only">還原</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {q.data.entries.map((e) => {
              const plan = restorePlan(e);
              return (
                <tr key={e.id} data-testid={'audit-' + e.id}>
                  <td className="nowrap">{fmtDateTimeShort(e.ts)}</td>
                  <td className="mono small">{e.ip}</td>
                  <td className="nowrap">{ACTION_LABEL[e.action] ?? e.action}</td>
                  <td className="mono small wrap-any">{e.target}</td>
                  <td className="small wrap-any">{summarize(e)}</td>
                  <td>
                    {plan && (
                      <button type="button" className={'btn small' + (plan.danger ? ' danger-text' : '')} onClick={() => runRestore(e).then(() => q.refetch())}>
                        {plan.label}
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
