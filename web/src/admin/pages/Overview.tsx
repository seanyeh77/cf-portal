import { useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { fmtDateTimeShort, relTime } from '../../shared/format';
import { copyText, Tip } from '../../shared/ui';
import { go } from '../App';
import { act, useRecords } from '../lib/data';
import { EXPIRY_KINDS, LEVEL_NAME, rowLevel } from '../lib/records';
import { modalStore, openEditor, setFilters } from '../lib/store';
import type { RecordsData } from '../types';

function view(kind: string) {
  if (EXPIRY_KINDS.has(kind)) setFilters({ expiry: kind }, true);
  else setFilters({ health: kind }, true);
  go('records');
}

function IssueList({ data, levels }: { data: RecordsData; levels: string[] }) {
  const rows = data.kind_order.filter((k) => levels.includes(data.kinds[k].level) && (data.summary[k] ?? 0) > 0);
  return (
    <>
      {rows.map((k) => {
        const kind = data.kinds[k];
        return (
          <div key={k} className={'issue-row bar-' + kind.level} data-testid={'ov-' + k}>
            <span className="issue-name" tabIndex={0} data-tip={kind.help}>
              {kind.label}
            </span>
            <span className="issue-count">{data.summary[k]} 筆</span>
            <span className={'small lv-' + kind.level}>{LEVEL_NAME[kind.level]}</span>
            <span className="spacer" />
            <button type="button" className="btn small" onClick={() => view(k)}>
              查看
            </button>
          </div>
        );
      })}
      {levels.includes('high') && data.orphans.length > 0 && (
        <div className="issue-row bar-medium">
          <span className="issue-name" tabIndex={0} data-tip="入口上架的服務對應的 record 已不存在，請下架。">
            入口服務已失效
          </span>
          <span className="issue-count">{data.orphans.length} 筆</span>
          <span className="small lv-medium">注意</span>
          <span className="spacer" />
          <button type="button" className="btn small" onClick={() => go('records')}>
            查看
          </button>
        </div>
      )}
      {rows.length === 0 && !(levels.includes('high') && data.orphans.length) && (
        <div className={levels.includes('high') ? 'okbox' : 'muted small'}>{levels.includes('high') ? '目前無待處理項目' : '無參考項目'}</div>
      )}
    </>
  );
}

export function Overview() {
  const { data, error } = useRecords();
  const [starting, setStarting] = useState(false);
  if (error) return <div className="errbox">無法讀取 Cloudflare 資料：{errMsg(error)}</div>;
  if (!data) return <p className="muted">讀取中…</p>;
  const now = Date.now() / 1000;
  const todo = data.records.filter((r) => rowLevel(r) !== '');
  const urgent = data.records.filter((r) => rowLevel(r) === 'high').length;
  const listed = data.records.filter((r) => r.listing).length;
  const scan = data.scan;
  const enabled = data.settings.requests_enabled === '1';
  const hours = Math.round(scan.interval / 3600);

  const startScan = async () => {
    setStarting(true);
    await act(() => api('POST', '/api/scan'), '已開始連線檢查');
    setStarting(false);
  };

  return (
    <div className="overview">
      <div className="stats">
        <div className="stat">
          <div className="stat-label">DNS record</div>
          <div className="stat-value">{data.records.length}</div>
        </div>
        <div className={'stat' + (urgent ? ' urgent' : '')}>
          <div className="stat-label">待處理</div>
          <div className="stat-value">
            {todo.length}
            {urgent > 0 && <span className="stat-note lv-high">（{urgent} 筆緊急）</span>}
          </div>
        </div>
        <div className="stat">
          <div className="stat-label">待審申請</div>
          <div className="stat-value">{data.pending_requests}</div>
        </div>
        <div className="stat">
          <div className="stat-label">入口服務</div>
          <div className="stat-value">{listed}</div>
        </div>
        <div className="stat">
          <div className="stat-label">最新快照</div>
          <div className="stat-value small-value" data-tip={data.last_snapshot ? fmtDateTimeShort(data.last_snapshot) : undefined}>
            {data.last_snapshot ? relTime(data.last_snapshot, now) : '尚無'}
          </div>
        </div>
      </div>

      <div className="ov-grid">
        <div className="ov-main">
          <section className="panel">
            <h2 className="panel-title">
              待處理 <Tip text="需要處理的問題，依嚴重程度排列。按「查看」會切換到 DNS Records 並套用該問題的篩選。" />
            </h2>
            <IssueList data={data} levels={['high', 'medium', 'low']} />
          </section>
          <section className="panel">
            <h2 className="panel-title">
              參考資訊 <Tip text="不一定需要處理的狀況，例如指向內網位址或多筆指向相同目標。" />
            </h2>
            <IssueList data={data} levels={['info']} />
          </section>
        </div>
        <aside className="ov-side">
          <section className="panel">
            <h2 className="panel-title">常用操作</h2>
            <div className="quick">
              <button type="button" className="btn primary block" onClick={() => openEditor()}>
                新增 Record
              </button>
              <button type="button" className="btn block" onClick={() => modalStore.set({ kind: 'batch', mode: 'rule' })}>
                批次新增
              </button>
              <button type="button" className="btn block" onClick={() => modalStore.set({ kind: 'batch', mode: 'csv' })}>
                匯入 CSV
              </button>
              <a className="btn block" href="/api/export.csv">
                匯出交接清單
              </a>
            </div>
          </section>
          <section className="panel">
            <h2 className="panel-title">
              連線檢查{' '}
              <Tip
                text={`定期對 A、AAAA、CNAME record 的主機嘗試連線（連接埠 ${data.settings.scan_ports}），並檢查 CNAME 目標是否存在。連續 ${data.settings.zombie_days} 日無回應會列為「長期無回應」。`}
              />
            </h2>
            <p className="small">
              上次：{scan.last_ts ? `${relTime(scan.last_ts, now)}（${scan.count} 筆）` : '尚未執行'}　週期：每 {hours >= 1 ? `${hours} 小時` : `${Math.round(scan.interval / 60)} 分鐘`}
            </p>
            {!scan.dns_ok && <div className="warnbox small">外部 DNS 查詢失敗，CNAME 目標檢查結果暫不可靠。</div>}
            {scan.error && <div className="errbox small">{scan.error}</div>}
            <button type="button" className="btn" disabled={scan.running || starting} onClick={startScan}>
              {scan.running || starting ? '檢查中…' : '立即檢查'}
            </button>
          </section>
          <section className="panel">
            <h2 className="panel-title">申請網域</h2>
            {enabled ? (
              <>
                <p className="small">待審：{data.pending_requests} 件</p>
                <div className="row-gap">
                  <button type="button" className="btn" onClick={() => go('requests')}>
                    查看申請
                  </button>
                  <button
                    type="button"
                    className="btn"
                    onClick={() => copyText(window.location.origin + '/request', '已複製申請頁連結')}
                  >
                    複製申請頁連結
                  </button>
                </div>
              </>
            ) : (
              <p className="small muted">未開放（可於網站設定啟用）</p>
            )}
          </section>
        </aside>
      </div>
    </div>
  );
}
