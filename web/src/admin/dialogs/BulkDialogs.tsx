import { useMemo, useState, type ReactNode } from 'react';
import { api, errMsg } from '../../shared/api';
import { confirmDialog, Dialog, DialogActions, SuggestInput, Tip, toast, toggled } from '../../shared/ui';
import { FailedList, runBulk, useBulk } from '../lib/bulk';
import {
  categoriesOf,
  cnameTargetCounts,
  defaultListingTitle,
  DNS_FIELD_LABEL,
  dnsRisks,
  fieldValue,
  pickRecords,
  proxyWarning,
  splitTags,
  sub,
  TTL_OPTIONS,
} from '../lib/records';
import { closeModal } from '../lib/store';
import type { RecordInput, RecordsData } from '../types';

// Bulk dialogs keep the selection, so the next action can follow.

/** Which optional fields are ticked, and tick(k): the OptRow props for field k. */
export function useOpts<K extends string>(init: Record<K, boolean>) {
  const [on, setOn] = useState(init);
  const tick = (k: K) => ({ on: on[k], setOn: (v: boolean) => setOn((p) => ({ ...p, [k]: v })) });
  return [on, tick] as const;
}

/** A labelled input that applies only when its checkbox is ticked. */
export function OptRow({ on, setOn, label, tip, children }: { on: boolean; setOn: (v: boolean) => void; label: string; tip?: string; children: ReactNode }) {
  return (
    <div className="bulk-row">
      <label className="check">
        <input type="checkbox" checked={on} onChange={(e) => setOn(e.target.checked)} aria-label={'修改' + label} />
        {label}
        {tip && <Tip text={tip} />}
      </label>
      <div className="bulk-input">{children}</div>
    </div>
  );
}

/** Buttons that append one of options, marked with sign, to a comma separated tag field. */
export function TagButtons({ options, value, onChange, sign = '+' }: { options: string[]; value: string; onChange: (v: string) => void; sign?: string }) {
  const cur = splitTags(value);
  return (
    <div className="tag-adds small">
      {options
        .filter((t) => !cur.includes(t))
        .map((t) => (
          <button key={t} type="button" className="linkbtn" onClick={() => onChange([...cur, t].join(', '))}>
            {sign} {t}
          </button>
        ))}
    </div>
  );
}

// ---- DNS ----

type DnsChange = { id: string; name: string; before: RecordInput; after?: RecordInput; changed: string[]; skipped?: string; error?: string; ok: boolean };

export function BulkDnsDialog({ ids, data }: { ids: string[]; data: RecordsData }) {
  const recs = pickRecords(data, ids);
  const [on, tick] = useOpts({ content: false, proxied: false, ttl: false });
  const [content, setContent] = useState('');
  const [proxied, setProxied] = useState('on');
  const [ttl, setTtl] = useState(1);
  const [plan, setPlan] = useState<DnsChange[] | null>(null);
  const [planning, setPlanning] = useState(false);
  const [error, setError] = useState('');
  const bulk = useBulk(closeModal);
  const cnameCounts = useMemo(() => cnameTargetCounts(data.records), [data.records]);

  const set: Record<string, unknown> = {};
  if (on.content) set.content = content.trim();
  if (on.proxied) set.proxied = proxied === 'on';
  if (on.ttl) set.ttl = ttl;

  const preview = async () => {
    setError('');
    if (!Object.keys(set).length) return setError('請勾選要修改的欄位');
    if (on.content && !content.trim()) return setError('請填寫新的指向目標');
    setPlanning(true);
    try {
      setPlan((await api<{ results: DnsChange[] }>('POST', '/api/records/bulk-dns', { ids, set, dry_run: true })).results);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setPlanning(false);
    }
  };

  const changes = plan?.filter((c) => c.after) ?? [];
  const apply = () => bulk.run('/api/records/bulk-dns', { ids, set }, (n) => `已修改 ${n} 筆，連線檢查中`);
  const failed = bulk.failed;

  if (plan) {
    const byID = new Map(recs.map((r) => [r.id, r]));
    const left = plan.filter((c) => !c.after);
    return (
      <Dialog
        title={`確認 DNS 變更（${changes.length} 筆）`}
        onClose={closeModal}
        width={760}
        testId="bulk-dns-preview"
        footer={
          <DialogActions
            error={error}
            onCancel={closeModal}
            confirm={failed.length ? '關閉' : `套用 ${changes.length} 筆`}
            disabled={bulk.busy || (!failed.length && !changes.length)}
            onConfirm={failed.length ? closeModal : apply}
            extra={
              !failed.length && (
                <button type="button" className="btn" onClick={() => setPlan(null)}>
                  返回修改
                </button>
              )
            }
          />
        }
      >
        <FailedList items={failed} />
        {changes.length > 0 && (
          <table className="plain diff bulk-diff">
            <thead>
              <tr>
                <th>名稱</th>
                <th>變更</th>
              </tr>
            </thead>
            <tbody>
              {changes.map((c) => {
                const rec = byID.get(c.id);
                const risks = rec && c.after ? dnsRisks(rec, c.after, cnameCounts) : [];
                const warn = c.after ? proxyWarning(c.after.type, c.after.proxied, c.after.content) : '';
                return (
                  <tr key={c.id} data-testid={'plan-' + sub(c.name, data.zone)}>
                    <td className="mono">{sub(c.name, data.zone)}</td>
                    <td>
                      {c.changed.map((f) => (
                        <div key={f} className="mono small wrap-any">
                          {DNS_FIELD_LABEL[f] ?? f}：<del className="old">{fieldValue(f, c.before[f as keyof RecordInput])}</del> →{' '}
                          <strong className={risks.length ? 'lv-high' : undefined}>{fieldValue(f, c.after![f as keyof RecordInput])}</strong>
                        </div>
                      ))}
                      {warn && <div className="small lv-medium">注意：{warn}</div>}
                      {risks.map((r) => (
                        <div key={r} className="small lv-medium">
                          注意：{r}
                        </div>
                      ))}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        {left.length > 0 && (
          <>
            <h3 className="sub-title">不會變更（{left.length} 筆）</h3>
            <ul className="bulk-list small">
              {left.map((c) => (
                <li key={c.id}>
                  <span className="mono">{sub(c.name, data.zone)}</span>：{c.error ?? c.skipped}
                </li>
              ))}
            </ul>
          </>
        )}
        <p className="small muted">套用前將自動保存快照；每筆變更會個別記錄，可在操作紀錄逐筆還原。</p>
      </Dialog>
    );
  }

  return (
    <Dialog
      title={`修改 DNS 設定（${recs.length} 筆）`}
      onClose={closeModal}
      width={600}
      testId="bulk-dns"
      footer={<DialogActions error={error} onCancel={closeModal} confirm="預覽變更" disabled={planning} onConfirm={preview} />}
    >
      <p className="small muted">僅修改勾選的欄位。</p>
      <OptRow
        {...tick('content')}
        label="指向目標"
        tip="IPv4 位址改為 A，IPv6 位址改為 AAAA，主機名稱改為 CNAME。只套用於 A、AAAA、CNAME。"
      >
        <input
          className="input mono"
          disabled={!on.content}
          value={content}
          aria-label="指向目標"
          placeholder="IP 位址或主機名稱"
          onChange={(e) => setContent(e.target.value)}
        />
      </OptRow>
      <OptRow {...tick('proxied')} label="Proxy" tip="只套用於 A、AAAA、CNAME。啟用時 TTL 固定為自動。">
        <select className="input" disabled={!on.proxied} value={proxied} aria-label="Proxy" onChange={(e) => setProxied(e.target.value)}>
          <option value="on">啟用</option>
          <option value="off">停用</option>
        </select>
      </OptRow>
      <OptRow {...tick('ttl')} label="TTL">
        <select className="input" disabled={!on.ttl} value={ttl} aria-label="TTL" onChange={(e) => setTtl(Number(e.target.value))}>
          {TTL_OPTIONS.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
      </OptRow>
    </Dialog>
  );
}

// ---- portal listings ----

export function BulkListCreateDialog({ ids, data }: { ids: string[]; data: RecordsData }) {
  const all = pickRecords(data, ids);
  const recs = all.filter((r) => !r.listing);
  const [rows, setRows] = useState(() => recs.map((r) => ({ id: r.id, title: defaultListingTitle(r, data.zone), url: '' })));
  const [category, setCategory] = useState('');
  const [tags, setTags] = useState('');
  const [featured, setFeatured] = useState(false);
  const [icon, setIcon] = useState('auto');
  const { busy, failed, run } = useBulk(closeModal);
  const categories = useMemo(() => categoriesOf(data.records), [data.records]);
  const setRow = (i: number, k: 'title' | 'url', v: string) => setRows((p) => p.map((r, j) => (j === i ? { ...r, [k]: v } : r)));

  const save = () => {
    const items = rows.map((r) => ({ record_id: r.id, title: r.title, url: r.url.trim() }));
    run('/api/listings/bulk-create', { items, category, tags: splitTags(tags), featured, icon }, (n) => `已上架 ${n} 筆`);
  };

  return (
    <Dialog
      title={`上架到服務入口（${recs.length} 筆）`}
      onClose={closeModal}
      width={760}
      testId="bulk-list"
      footer={<DialogActions onCancel={closeModal} confirm={`上架 ${recs.length} 筆`} disabled={busy || !recs.length} onConfirm={save} />}
    >
      <FailedList items={failed} />
      {all.length > recs.length && <p className="small muted">另有 {all.length - recs.length} 筆已上架，將略過。</p>}
      <div className="two">
        <div className="field">
          <label htmlFor="bl-cat">分類</label>
          <SuggestInput id="bl-cat" options={categories} value={category} maxLength={20} onChange={(e) => setCategory(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="bl-icon">圖示</label>
          <select id="bl-icon" className="input" value={icon} onChange={(e) => setIcon(e.target.value)}>
            <option value="auto">自動取得網站圖示</option>
            <option value="none">不顯示圖示</option>
          </select>
        </div>
      </div>
      <div className="field">
        <label htmlFor="bl-tags">
          標籤 <Tip text="以逗號分隔，最多 6 個。" />
        </label>
        <input id="bl-tags" className="input" value={tags} onChange={(e) => setTags(e.target.value)} />
        <TagButtons options={data.preset_tags} value={tags} onChange={setTags} />
      </div>
      <label className="check">
        <input type="checkbox" checked={featured} onChange={(e) => setFeatured(e.target.checked)} />
        設為精選
      </label>
      <table className="plain top-gap bulk-items">
        <thead>
          <tr>
            <th>名稱</th>
            <th>顯示名稱</th>
            <th>
              網址 <Tip text="留空則使用 https://名稱。" />
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => {
            const rec = recs[i];
            return (
              <tr key={r.id}>
                <td className="mono small">{sub(rec.name, data.zone)}</td>
                <td>
                  <input className="input" value={r.title} maxLength={40} aria-label={'顯示名稱 ' + rec.name} onChange={(e) => setRow(i, 'title', e.target.value)} />
                </td>
                <td>
                  <input className="input mono" value={r.url} placeholder={'https://' + rec.name} aria-label={'網址 ' + rec.name} onChange={(e) => setRow(i, 'url', e.target.value)} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </Dialog>
  );
}

export function BulkListEditDialog({ ids, data }: { ids: string[]; data: RecordsData }) {
  const recs = pickRecords(data, ids).filter((r) => r.listing);
  const [on, tick] = useOpts({ category: false, featured: false });
  const [category, setCategory] = useState('');
  const [featured, setFeatured] = useState('on');
  const [add, setAdd] = useState('');
  const [remove, setRemove] = useState('');
  const [error, setError] = useState('');
  const { busy, failed, run } = useBulk(closeModal);
  const categories = useMemo(() => categoriesOf(data.records), [data.records]);
  const used = [...new Set(recs.flatMap((r) => r.listing!.tags))].sort();

  const save = () => {
    const body: Record<string, unknown> = { ids: recs.map((r) => r.id), add_tags: splitTags(add), remove_tags: splitTags(remove) };
    if (on.category) body.category = category;
    if (on.featured) body.featured = featured === 'on';
    if (!on.category && !on.featured && !splitTags(add).length && !splitTags(remove).length) return setError('請勾選或填寫要修改的欄位');
    setError('');
    run('/api/listings/bulk-update', body, (n) => `已更新 ${n} 筆入口設定`);
  };

  return (
    <Dialog
      title={`修改入口設定（${recs.length} 筆）`}
      onClose={closeModal}
      width={600}
      testId="bulk-list-edit"
      footer={<DialogActions error={error} onCancel={closeModal} confirm="儲存" disabled={busy || !recs.length} onConfirm={save} />}
    >
      <FailedList items={failed} />
      <p className="small muted">僅修改勾選或填寫的欄位；標籤會加入或移除，不會覆蓋各服務原有的其他標籤。</p>
      <OptRow {...tick('category')} label="分類">
        <SuggestInput options={categories} disabled={!on.category} value={category} maxLength={20} aria-label="分類" onChange={(e) => setCategory(e.target.value)} />
      </OptRow>
      <OptRow {...tick('featured')} label="精選">
        <select className="input" disabled={!on.featured} value={featured} aria-label="精選" onChange={(e) => setFeatured(e.target.value)}>
          <option value="on">設為精選</option>
          <option value="off">取消精選</option>
        </select>
      </OptRow>
      <div className="field">
        <label htmlFor="ble-add">加入標籤</label>
        <input id="ble-add" className="input" value={add} onChange={(e) => setAdd(e.target.value)} />
        <TagButtons options={data.preset_tags} value={add} onChange={setAdd} />
      </div>
      <div className="field">
        <label htmlFor="ble-remove">移除標籤</label>
        <input id="ble-remove" className="input" value={remove} onChange={(e) => setRemove(e.target.value)} />
        <TagButtons options={used} value={remove} onChange={setRemove} sign="−" />
      </div>
    </Dialog>
  );
}

/** Asks, then takes the listed records among ids off the portal. */
export async function bulkUnlist(ids: string[], data: RecordsData) {
  const recs = pickRecords(data, ids).filter((r) => r.listing);
  const ok = await confirmDialog({
    title: `從入口下架 ${recs.length} 筆`,
    body: (
      <ul className="bulk-list small">
        {recs.map((r) => (
          <li key={r.id}>
            {r.listing!.title} <span className="mono faint">{r.name}</span>
          </li>
        ))}
      </ul>
    ),
    confirmText: '下架',
    danger: true,
  });
  if (ok) await runBulk('/api/listings/bulk-delete', { ids: recs.map((r) => r.id) }, (n) => `已下架 ${n} 筆`);
}

// ---- issues ----

export function BulkMuteDialog({ ids, data }: { ids: string[]; data: RecordsData }) {
  const recs = pickRecords(data, ids);
  const count = (kind: string, muted: boolean) => recs.filter((r) => (muted ? r.muted : r.issues).some((i) => i.kind === kind)).length;
  const active = data.kind_order.filter((k) => count(k, false) > 0);
  const muted = data.kind_order.filter((k) => count(k, true) > 0);
  const [mute, setMute] = useState<Set<string>>(new Set());
  const [unmute, setUnmute] = useState<Set<string>>(new Set());
  const [note, setNote] = useState('');
  const [error, setError] = useState('');
  const { busy, failed, run } = useBulk(closeModal);

  const save = () => {
    if (!mute.size && !unmute.size) return setError('請勾選要忽略或還原的項目');
    setError('');
    run('/api/issues/bulk-mute', { ids, mute: [...mute], unmute: [...unmute], note }, (n) => `已更新 ${n} 筆`);
  };

  const list = (kinds: string[], isMuted: boolean) =>
    kinds.map((k) => (
      <label key={k} className="check mute-row">
        <input
          type="checkbox"
          checked={(isMuted ? unmute : mute).has(k)}
          onChange={(e) => (isMuted ? setUnmute : setMute)((p) => toggled(p, k, e.target.checked))}
        />
        {data.kinds[k].label}
        <span className="muted small">{count(k, isMuted)} 筆</span>
      </label>
    ));

  return (
    <Dialog
      title={`忽略問題（${recs.length} 筆）`}
      onClose={closeModal}
      width={560}
      testId="bulk-mute"
      footer={<DialogActions error={error} onCancel={closeModal} confirm="儲存" disabled={busy} onConfirm={save} />}
    >
      <FailedList items={failed} />
      {active.length === 0 && muted.length === 0 && <p className="muted">選取的 record 沒有可忽略的項目。</p>}
      {active.length > 0 && (
        <>
          <h3 className="sub-title">
            忽略 <Tip text="忽略後不再列入狀況與統計，可隨時還原。" />
          </h3>
          {list(active, false)}
        </>
      )}
      {muted.length > 0 && (
        <>
          <h3 className="sub-title">還原已忽略的項目</h3>
          {list(muted, true)}
        </>
      )}
      <div className="field top-gap">
        <label htmlFor="bm-note">
          原因（選填） <Tip text="會附加到被忽略的 record 的備註，方便日後交接。" />
        </label>
        <input id="bm-note" className="input" value={note} maxLength={300} placeholder="例：大檔案上傳需要，故不經 Proxy" onChange={(e) => setNote(e.target.value)} />
      </div>
    </Dialog>
  );
}

/** Queues a connection check for each record now. */
export async function bulkScan(ids: string[]) {
  await runBulk('/api/records/bulk-scan', { ids }, (n) => `${n} 筆連線檢查中`);
}

/** Downloads the selected records in the handover CSV format. */
export function bulkExport(ids: string[]) {
  if (!ids.length) return toast('未選擇任何 record', { error: true });
  window.location.assign('/api/export.csv?ids=' + ids.map(encodeURIComponent).join(','));
}
