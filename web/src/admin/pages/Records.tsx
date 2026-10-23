import { Fragment, memo, useEffect, useMemo, type ReactNode } from 'react';
import { api, errMsg } from '../../shared/api';
import { relTime } from '../../shared/format';
import { expiryPresets, type ExpiryCycle } from '../lib/expiry';
import { bulkExport, bulkScan, bulkUnlist } from '../dialogs/BulkDialogs';
import { MenuButton, Tip, toast, toggled, TriCheck, type MenuItem } from '../../shared/ui';
import { act, refreshRecords, useRecords } from '../lib/data';
import {
  describeFilters,
  dotState,
  EMPTY_FILTERS,
  expiryView,
  EXPIRY_KINDS,
  EXPIRY_LABEL,
  groupLabel,
  hasIssue,
  LEVEL_NAME,
  matches,
  PREFIX,
  sortRecords,
  statusIssues,
  sub,
  syncedTip,
  distinctOf,
  tunnelName,
  tunnelStatus,
  type SortKey,
} from '../lib/records';
import {
  modalStore,
  openDrawer,
  openEditor,
  useNow,
  selectionStore,
  setFilters,
  useStore,
  viewStore,
} from '../lib/store';
import type { DNSRecord, Issue, RecordsData } from '../types';

export function Records() {
  const { data, error, isLoading } = useRecords();
  if (error) return <div className="errbox">無法讀取 Cloudflare 資料：{errMsg(error)}</div>;
  if (isLoading || !data) return <p className="muted">讀取中…</p>;
  return <RecordsView data={data} />;
}

const saveMeta = (id: string, patch: Record<string, unknown>, msg: string) => act(() => api('PUT', `/api/records/${id}/meta`, patch), msg);

function extendItems(r: DNSRecord, today: string, cycle: ExpiryCycle): MenuItem[] {
  const cur = r.meta.expires_at;
  const base = cur && cur > today ? cur : today;
  return [
    ...expiryPresets(base, cycle).map((p) => ({
      label: `${p.extend}（${p.date}）`,
      onClick: () => saveMeta(r.id, { expires_at: p.date }, `已延長到期日至 ${p.date}`),
    })),
    { label: '取消到期日', onClick: () => saveMeta(r.id, { expires_at: '', auto_delete: false }, '已取消到期日') },
    'sep',
    { label: '自訂…', onClick: () => openEditor(r.id, 'expires') },
  ];
}

type Fix = { label: string; run: (r: DNSRecord, i: Issue) => void; needsEditor: boolean };
const editTarget: Fix = { label: '編輯目標', run: (r) => openEditor(r.id, 'content'), needsEditor: true };

/** One-click fixes shown after the top issue in the status column. */
const FIX_LINK: Record<string, Fix> = {
  bad_target: editTarget,
  dangling: editTarget,
  unclaimed: editTarget,
  loopback: editTarget,
  tunnel_missing: editTarget,
  tunnel_unproxied: { label: '編輯 Proxy', run: (r) => openEditor(r.id, 'proxied'), needsEditor: true },
  upstream_broken: { label: '前往上游', run: (_, i) => i.upstream && openDrawer(i.upstream), needsEditor: false },
  proxied_private: { label: '編輯 Proxy', run: (r) => openEditor(r.id, 'proxied'), needsEditor: true },
  name_rule: { label: '編輯名稱', run: (r) => openEditor(r.id, 'name'), needsEditor: true },
  unproxied_web: { label: '編輯', run: (r) => openEditor(r.id), needsEditor: true },
  zombie: { label: '停用檢查', run: (r) => saveMeta(r.id, { no_scan: true }, '已停用連線檢查'), needsEditor: false },
};

function StatusCell({ r, pending }: { r: DNSRecord; pending: boolean }) {
  const shown = statusIssues(r);
  const top = shown[0];
  const rest = shown.slice(1);
  const fix = top && FIX_LINK[top.kind];
  return (
    <div className="status-cell">
      {pending && (
        <div className="checking">
          <span className="spin" aria-hidden /> 連線檢查中
        </div>
      )}
      {top && (
        <div className="status-line">
          <span className={'lv-' + top.level + ' status-label'} tabIndex={0} data-tip={[top.detail, top.help].filter(Boolean).join('\n')}>
            {PREFIX[top.level] ?? ''}
            {top.label}
          </span>
          {rest.length > 0 && (
            <span className="faint more" tabIndex={0} data-tip={rest.map((i) => (PREFIX[i.level] ?? '') + i.label).join('\n')}>
              等 {rest.length} 項
            </span>
          )}
          {fix && (!fix.needsEditor || r.editable) && (
            <button type="button" className="linkbtn fix" onClick={() => fix.run(r, top)}>
              {fix.label}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function ExpiryCell({ r, today, cycle }: { r: DNSRecord; today: string; cycle: ExpiryCycle }) {
  const v = expiryView(r, today);
  if (!v) return null;
  return (
    <div className="expiry-cell">
      <span className={v.cls} tabIndex={0} data-tip={v.tip}>
        {v.text}
      </span>
      {v.extend && (
        <MenuButton label="延長" className="linkbtn" items={extendItems(r, today, cycle)} align="left" testId={'extend-' + r.id} />
      )}
    </div>
  );
}

function SortHead({ k, label, sort, tip }: { k: SortKey; label: string; sort: { key: SortKey; dir: string }; tip?: ReactNode }) {
  const active = sort.key === k;
  const arrow = active ? (sort.dir === 'desc' ? '↓' : '↑') : '↕';
  const toggle = () =>
    viewStore.set((v) => ({
      ...v,
      sort: { key: k, dir: v.sort.key === k ? (v.sort.dir === 'asc' ? 'desc' : 'asc') : k === 'status' ? 'desc' : 'asc' },
    }));
  return (
    <th aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <span className="th-inner">
        <button type="button" className={'sort' + (active ? ' active' : '')} onClick={toggle}>
          {label} <span className="arrow">{arrow}</span>
        </button>
        {tip}
      </span>
    </th>
  );
}

/** "更新於 …" for the last Cloudflare sync; ticks on the shared clock. */
function SyncedAt({ at }: { at: number }) {
  return (
    <span className="small muted" tabIndex={0} data-tip={syncedTip(at)}>
      更新於 {relTime(at, useNow())}
    </span>
  );
}

/** The connection dot; reads the shared clock so rows need not re-render. */
function ConnDot({ r, pending }: { r: DNSRecord; pending: boolean }) {
  const dot = dotState(r, pending, useNow());
  return <span className={'dot ' + dot.state} data-tip={dot.tip || undefined} tabIndex={dot.tip ? 0 : undefined} aria-label={dot.tip || undefined} />;
}

/** Selects or clears all of rows; shows a mixed state when only some are selected. */
function SelectAll({ rows, selection, label }: { rows: DNSRecord[]; selection: Set<string>; label: string }) {
  return (
    <TriCheck
      total={rows.length}
      selected={rows.filter((r) => selection.has(r.id)).length}
      label={label}
      onChange={(all) =>
        selectionStore.set((prev) => toggled(prev, rows.map((r) => r.id), all))
      }
    />
  );
}

/** One table row. Memoized: polling keeps unchanged record objects stable. */
const Row = memo(function Row({
  r,
  zone,
  today,
  cycle,
  selected,
  pending,
  showExpiry,
}: {
  r: DNSRecord;
  zone: string;
  today: string;
  cycle: ExpiryCycle;
  selected: boolean;
  pending: boolean;
  showExpiry: boolean;
}) {
  const lv = r.level;
  const s = sub(r.name, zone);
  const unassigned = !r.meta.owner.trim();
  const metaLine = [r.meta.purpose, r.meta.owner].filter(Boolean).join(' · ');
  const menu: MenuItem[] = [
    { label: '詳細資料', onClick: () => openDrawer(r.id) },
    { label: r.listing ? '入口設定' : '上架到入口', onClick: () => modalStore.set({ kind: 'listing', id: r.id }) },
    'sep',
    { label: '刪除', danger: true, onClick: () => modalStore.set({ kind: 'delete', id: r.id }) },
  ];
  return (
    <tr
      className={'row' + (lv === 'high' || lv === 'medium' ? ' bar-' + lv : '') + (selected ? ' selected' : '')}
      data-testid={'row-' + s}
      onClick={(e) => {
        if ((e.target as Element).closest('a,button,input,label,select,textarea,[role=menu]')) return;
        if (window.getSelection()?.toString()) return;
        openDrawer(r.id);
      }}
    >
      <td className="c-check">
        <label className="check-hit" aria-label={'選取 ' + r.name}>
          <input
            type="checkbox"
            checked={selected}
            onChange={(e) =>
              selectionStore.set((prev) => toggled(prev, r.id, e.target.checked))
            }
          />
        </label>
      </td>
      <td className="c-type mono faint">{r.type}</td>
      <td className="c-name">
        <div className="name-line">
          <span className="mono name-sub">{s}</span>
          {s !== '@' && <span className="mono zone-suffix">.{zone}</span>}
        </div>
        <div className="meta-line small">
          {unassigned ? (
            <>
              {r.meta.purpose && <span className="muted">{r.meta.purpose} · </span>}
              <span className="muted">未指派負責人</span>
              <button
                type="button"
                className="linkbtn assign"
                onClick={() => modalStore.set({ kind: 'bulkMeta', ids: [r.id], focusOwner: true })}
              >
                指派
              </button>
            </>
          ) : (
            <span className="muted" tabIndex={r.meta.notes ? 0 : undefined} data-tip={r.meta.notes || undefined}>
              {metaLine}
            </span>
          )}
        </div>
        {r.listing && (
          <div className="small muted" data-tip={r.listing.effective_url}>
            入口：{r.listing.title}
          </div>
        )}
      </td>
      <td className="c-target">
        <div className="target-line">
          <ConnDot r={r} pending={pending} />
          {r.tunnel ? (
            <span className="target" tabIndex={0} data-tip={r.content}>
              Tunnel <span className="mono">{tunnelName(r.tunnel)}</span>
            </span>
          ) : (
            <span className="mono target" data-tip={r.content.length > 34 ? r.content : undefined}>
              {r.type === 'MX' && r.priority != null ? `${r.priority} ` : ''}
              {r.content}
            </span>
          )}
        </div>
        {(r.proxied || r.tunnel) && (
          <div className="small faint proxied">{[r.tunnel && tunnelStatus(r.tunnel), r.proxied && 'Proxied'].filter(Boolean).join(' · ')}</div>
        )}
      </td>
      <td className="c-status">
        <StatusCell r={r} pending={pending} />
      </td>
      {showExpiry && (
        <td className="c-expiry">
          <ExpiryCell r={r} today={today} cycle={cycle} />
        </td>
      )}
      <td className="c-actions">
        <div className="actions">
          <button
            type="button"
            className="btn small"
            disabled={!r.editable}
            data-tip={r.editable ? undefined : `${r.type} 請於 Cloudflare 編輯`}
            onClick={() => openEditor(r.id)}
          >
            編輯
          </button>
          <MenuButton label="⋯" className="btn small more-btn" ariaLabel={'更多操作 ' + r.name} items={menu} testId={'more-' + s} />
        </div>
      </td>
    </tr>
  );
});

function RecordsView({ data }: { data: RecordsData }) {
  const view = useStore(viewStore);
  const selection = useStore(selectionStore);
  const pending = useMemo(() => new Set(data.scan.pending), [data]);
  const f = view.filters;
  const zone = data.zone;

  const base = useMemo(() => data.records.filter((r) => matches(r, f)), [data, f]);
  const filtered = useMemo(() => base.filter((r) => !f.severity || r.level === f.severity), [base, f.severity]);
  const sorted = useMemo(() => sortRecords(filtered, view.sort, zone), [filtered, view.sort, zone]);
  const counts = useMemo(() => {
    const c = { all: base.length, high: 0, medium: 0, low: 0 };
    for (const r of base) {
      const lv = r.level;
      if (lv === 'high' || lv === 'medium' || lv === 'low') c[lv]++;
    }
    return c;
  }, [base]);
  const showExpiry = filtered.some((r) => r.meta.expires_at);
  const unassigned = data.records.filter((r) => hasIssue(r, 'no_owner'));
  const types = useMemo(() => distinctOf(data.records, (r) => r.type), [data.records]);
  const desc = describeFilters(f, data);
  const anyFilter = desc.length > 0;
  const allIds = useMemo(() => new Set(data.records.map((r) => r.id)), [data.records]);
  const selectedIds = Array.from(selection).filter((id) => allIds.has(id));
  const listedSel = data.records.filter((r) => r.listing && selection.has(r.id)).length;
  const unlistedSel = selectedIds.length - listedSel;

  // Clear selections of records that no longer exist.
  useEffect(() => {
    if (selectedIds.length !== selection.size) selectionStore.set(new Set(selectedIds));
  }, [selectedIds.length, selection.size]); // eslint-disable-line react-hooks/exhaustive-deps

  const kindOptions = data.kind_order.filter((k) => !EXPIRY_KINDS.has(k));
  const expiryCount = (k: string) =>
    k === 'has' ? data.records.filter((r) => r.meta.expires_at).length : k === 'none' ? data.records.filter((r) => !r.meta.expires_at).length : data.summary[k] ?? 0;

  const cols = showExpiry ? 7 : 6;

  // Targets shared by two or more records (any filter), for grouping.
  const targetCounts = useMemo(() => {
    const n = new Map<string, number>();
    data.records.forEach((r) => r.target_key && n.set(r.target_key, (n.get(r.target_key) ?? 0) + 1));
    return n;
  }, [data.records]);

  const groups = useMemo(() => {
    if (!view.byTarget) return null;
    const by = new Map<string, DNSRecord[]>();
    const other: DNSRecord[] = [];
    for (const r of sorted) {
      const k = r.target_key;
      if (k && (targetCounts.get(k) ?? 0) >= 2) {
        if (!by.has(k)) by.set(k, []);
        by.get(k)!.push(r);
      } else other.push(r);
    }
    const out = Array.from(by.entries()).sort((a, b) => a[0].localeCompare(b[0]));
    return { out, other };
  }, [view.byTarget, sorted, targetCounts]);

  const { expiry_cycle_name, expiry_cycle_dates } = data.settings;
  // A stable object, so polling does not re-render every memoized row.
  const cycle = useMemo(() => ({ name: expiry_cycle_name, dates: expiry_cycle_dates }), [expiry_cycle_name, expiry_cycle_dates]);

  const renderRow = (r: DNSRecord) => (
    <Row
      key={r.id}
      r={r}
      zone={zone}
      today={data.today}
      cycle={cycle}
      selected={selection.has(r.id)}
      pending={pending.has(r.id)}
      showExpiry={showExpiry}
    />
  );

  return (
    <div className="records">
      <div className="filter-row">
        <input
          className="input search"
          type="search"
          placeholder="搜尋名稱、內容、負責人"
          aria-label="搜尋"
          value={f.q}
          onChange={(e) => setFilters({ q: e.target.value })}
        />
        <select className="input" aria-label="類型" value={f.type} onChange={(e) => setFilters({ type: e.target.value })}>
          <option value="">全部類型</option>
          {types.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <select className="input" aria-label="分組" value={f.group} onChange={(e) => setFilters({ group: e.target.value })}>
          <option value="">全部分組</option>
          {data.groups.map(([g, n]) => (
            <option key={g} value={g}>
              {groupLabel(g)}（{n}）
            </option>
          ))}
        </select>
        <select className="input" aria-label="健康狀態" value={f.health} onChange={(e) => setFilters({ health: e.target.value })}>
          <option value="">全部健康狀態</option>
          {kindOptions.map((k) => (
            <option key={k} value={k}>
              {data.kinds[k].label}（{data.summary[k] ?? 0}）
            </option>
          ))}
          <option value="listed">已上架到入口（{data.records.filter((r) => r.listing).length}）</option>
          <option value="muted">含已忽略項目（{data.records.filter((r) => r.muted.length).length}）</option>
        </select>
        <select className="input" aria-label="到期狀態" value={f.expiry} onChange={(e) => setFilters({ expiry: e.target.value })}>
          <option value="">全部到期狀態</option>
          {['expired', 'expiring', 'has', 'none'].map((k) => (
            <option key={k} value={k}>
              {EXPIRY_LABEL[k]}（{expiryCount(k)}）
            </option>
          ))}
        </select>
      </div>

      <div className="toolbar-row">
        <div className="severity">
          {(
            [
              ['', '全部', counts.all],
              ['high', LEVEL_NAME.high, counts.high],
              ['medium', LEVEL_NAME.medium, counts.medium],
              ['low', LEVEL_NAME.low, counts.low],
            ] as const
          ).map(([k, label, n], i) => (
            <Fragment key={k}>
              {i > 0 && <span className="faint sep">·</span>}
              <button
                type="button"
                className={'sev-link' + (f.severity === k ? ' active' : '') + (k ? ' sev-' + k : '')}
                aria-pressed={f.severity === k}
                onClick={() => setFilters({ severity: k })}
              >
                {label} {n}
              </button>
            </Fragment>
          ))}
          <Tip text="依每筆最嚴重的問題計算，每筆只算一次。" />
        </div>
        <div className="toolbar-right">
          <button type="button" className="linkbtn" onClick={() => viewStore.set((v) => ({ ...v, byTarget: !v.byTarget }))}>
            {view.byTarget ? '改為清單檢視' : '依目標分組'}
          </button>
          <SyncedAt at={data.synced_at} />
          <button
            type="button"
            className="btn"
            onClick={() =>
              refreshRecords(true).then(
                () => toast('已重新整理'),
                (e) => toast(errMsg(e), { error: true }),
              )
            }
          >
            重新整理
          </button>
          <MenuButton
            label="批次與匯入匯出 ▾"
            items={[
              { label: '批次新增', onClick: () => modalStore.set({ kind: 'batch', mode: 'rule' }) },
              { label: '匯入 CSV', onClick: () => modalStore.set({ kind: 'batch', mode: 'csv' }) },
              'sep',
              { label: '匯出交接清單', onClick: () => window.location.assign('/api/export.csv') },
            ]}
          />
          <button type="button" className="btn primary" onClick={() => openEditor()}>
            新增 Record
          </button>
        </div>
      </div>

      {anyFilter && (
        <div className="filter-desc">
          目前篩選：{desc.join('、')}，符合 {filtered.length} 筆
          <button type="button" className="linkbtn" onClick={() => setFilters(EMPTY_FILTERS, true)}>
            清除全部
          </button>
        </div>
      )}

      {data.tunnel_error && <div className="warnbox">注意：{data.tunnel_error}</div>}

      {data.orphans.length > 0 && (
        <div className="warnbox">
          下列入口服務的 record 已不存在，請下架：
          <ul className="orphans">
            {data.orphans.map((o) => (
              <li key={o.record_id}>
                {o.title} <span className="mono faint">{o.hostname}</span>{' '}
                <button
                  type="button"
                  className="btn small"
                  onClick={() => act(() => api('DELETE', `/api/listings/${o.record_id}`), '已下架')}
                >
                  下架
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {unassigned.length > 0 && (
        <div className="banner">
          <span>{unassigned.length} 筆未指派負責人</span>
          <button type="button" className="linkbtn" onClick={() => setFilters({ health: 'no_owner' }, true)}>
            查看
          </button>
          <button
            type="button"
            className="linkbtn"
            onClick={() => {
              const ids = unassigned.map((r) => r.id);
              selectionStore.set(new Set(ids));
              modalStore.set({ kind: 'bulkMeta', ids, focusOwner: true });
            }}
          >
            批次指派
          </button>
        </div>
      )}

      {selectedIds.length > 0 && (
        <div className="bulkbar" role="region" aria-label="批次操作">
          <span>已選 {selectedIds.length} 筆</span>
          <MenuButton
            label="管理資訊 ▾"
            className="linkbtn"
            align="left"
            testId="bulk-meta-menu"
            items={[
              { label: '設定管理資訊…', onClick: () => modalStore.set({ kind: 'bulkMeta', ids: selectedIds }) },
              'sep',
              ...expiryPresets(data.today, cycle).map((p) => ({
                label: `到期日：${p.label}（${p.date}）`,
                onClick: () => bulkSet(selectedIds, { expires_at: p.date }, `已將到期日設為 ${p.date}`),
              })),
              'sep',
              { label: '停用連線檢查', onClick: () => bulkSet(selectedIds, { no_scan: true }, '已停用連線檢查') },
              { label: '啟用連線檢查', onClick: () => bulkSet(selectedIds, { no_scan: false }, '已啟用連線檢查') },
              'sep',
              { label: '忽略問題…', onClick: () => modalStore.set({ kind: 'bulkMute', ids: selectedIds }) },
            ]}
          />
          <button type="button" className="linkbtn" onClick={() => modalStore.set({ kind: 'bulkDns', ids: selectedIds })}>
            修改 DNS
          </button>
          <MenuButton
            label="入口 ▾"
            className="linkbtn"
            align="left"
            testId="bulk-portal-menu"
            items={[
              {
                label: `上架到入口…（${unlistedSel} 筆）`,
                disabled: !unlistedSel,
                tip: unlistedSel ? undefined : '選取的 record 皆已上架',
                onClick: () => modalStore.set({ kind: 'bulkList', ids: selectedIds }),
              },
              {
                label: `修改入口設定…（${listedSel} 筆）`,
                disabled: !listedSel,
                tip: listedSel ? undefined : '選取的 record 皆未上架',
                onClick: () => modalStore.set({ kind: 'bulkListEdit', ids: selectedIds }),
              },
              {
                label: `下架（${listedSel} 筆）`,
                danger: true,
                disabled: !listedSel,
                tip: listedSel ? undefined : '選取的 record 皆未上架',
                onClick: () => bulkUnlist(selectedIds, data),
              },
            ]}
          />
          <button type="button" className="linkbtn" onClick={() => bulkScan(selectedIds)}>
            立即檢查
          </button>
          <button type="button" className="linkbtn" onClick={() => bulkExport(selectedIds)}>
            匯出
          </button>
          <button type="button" className="linkbtn" onClick={() => modalStore.set({ kind: 'bulkDelete', ids: selectedIds })}>
            刪除
          </button>
          <span className="spacer" />
          <button type="button" className="linkbtn" onClick={() => selectionStore.set(new Set())}>
            取消選取
          </button>
        </div>
      )}

      <div className="table-wrap">
        <table className="rec-table">
          <thead>
            <tr>
              <th className="c-check">
                <SelectAll rows={sorted} selection={selection} label="全選目前篩選結果" />
              </th>
              <SortHead k="type" label="類型" sort={view.sort} />
              <SortHead k="name" label="名稱" sort={view.sort} />
              <SortHead k="content" label="指向" sort={view.sort} />
              <SortHead
                k="status"
                label="狀況"
                sort={view.sort}
                tip={<Tip text="列出需處理的項目。左側色條：紅色為緊急，橘色為注意。點選欄位標題可排序。" />}
              />
              {showExpiry && <SortHead k="expiry" label="到期" sort={view.sort} />}
              <th className="c-actions">
                <span className="sr-only">操作</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && (
              <tr>
                <td colSpan={cols} className="empty">
                  無符合項目
                </td>
              </tr>
            )}
            {groups
              ? [
                  ...groups.out.map(([k, rows]) => (
                    <Fragment key={k}>
                      <tr className="group-head">
                        <td className="c-check">
                          <SelectAll rows={rows} selection={selection} label={`選取指向 ${rows[0].tunnel ? 'Tunnel ' + tunnelName(rows[0].tunnel) : k} 的 ${rows.length} 筆`} />
                        </td>
                        <td colSpan={cols - 1}>
                          → {rows[0].tunnel ? <>Tunnel <span className="mono">{tunnelName(rows[0].tunnel)}</span></> : <span className="mono">{k}</span>}（{rows.length} 筆）
                          <button
                            type="button"
                            className="linkbtn group-edit"
                            onClick={() => {
                              const ids = rows.map((r) => r.id);
                              selectionStore.set(new Set(ids));
                              modalStore.set({ kind: 'bulkMeta', ids });
                            }}
                          >
                            設定此組管理資訊
                          </button>
                        </td>
                      </tr>
                      {rows.map(renderRow)}
                    </Fragment>
                  )),
                  groups.other.length > 0 && (
                    <Fragment key="__other">
                      <tr className="group-head">
                        <td className="c-check">
                          <SelectAll rows={groups.other} selection={selection} label={`選取其他 ${groups.other.length} 筆`} />
                        </td>
                        <td colSpan={cols - 1}>其他（{groups.other.length} 筆）</td>
                      </tr>
                      {groups.other.map(renderRow)}
                    </Fragment>
                  ),
                ]
              : sorted.map(renderRow)}
          </tbody>
        </table>
      </div>
      <p className="small faint legend">
        連線圓點：<span className="dot ok" /> 正常　<span className="dot down" /> 無回應　<span className="dot none" /> 未檢查或已停用　共{' '}
        {data.records.length} 筆，{LEVEL_NAME.high} {counts.high} 筆
      </p>
    </div>
  );
}

const bulkSet = (ids: string[], set: Record<string, unknown>, msg: string) => act(() => api('POST', '/api/records/bulk-meta', { ids, set }), msg);
