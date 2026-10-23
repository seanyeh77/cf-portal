import { useEffect, useRef, useState } from 'react';
import { api } from '../../shared/api';
import { Dialog, DialogActions, SuggestInput, toast } from '../../shared/ui';
import { act, useRecords } from '../lib/data';
import { FailedList, useBulk } from '../lib/bulk';
import { ExpiryButtons } from '../lib/expiry';
import { ownersOf, pickRecords, sub } from '../lib/records';
import { closeModal, drawerStore, modalStore, selectionStore, useStore } from '../lib/store';
import type { DNSRecord, RecordsData } from '../types';
import { BatchDialog } from './BatchDialog';
import { BulkDnsDialog, BulkListCreateDialog, BulkListEditDialog, BulkMuteDialog, OptRow, useOpts } from './BulkDialogs';
import { ListingDialog } from './ListingDialog';
import { RecordDrawer } from './RecordDrawer';
import { RecordEditor } from './RecordEditor';

export function Modals() {
  const modal = useStore(modalStore);
  const drawerId = useStore(drawerStore);
  const { data } = useRecords();
  if (!data) return null;
  const find = (id?: string) => data.records.find((r) => r.id === id);
  const drawerRec = drawerId ? find(drawerId) : undefined;
  let dlg: React.ReactNode = null;
  if (modal) {
    switch (modal.kind) {
      case 'editor': {
        const rec = find(modal.id);
        if (!modal.id || rec) dlg = <RecordEditor key={modal.id ?? 'new'} record={rec} data={data} focus={modal.focus} />;
        break;
      }
      case 'listing': {
        const rec = find(modal.id);
        if (rec) dlg = <ListingDialog record={rec} data={data} />;
        break;
      }
      case 'delete': {
        const rec = find(modal.id);
        if (rec) dlg = <DeleteDialog r={rec} zone={data.zone} />;
        break;
      }
      case 'bulkMeta':
        dlg = <BulkMetaDialog ids={modal.ids} focusOwner={modal.focusOwner} data={data} />;
        break;
      case 'bulkDelete':
        dlg = <BulkDeleteDialog ids={modal.ids} data={data} />;
        break;
      case 'bulkDns':
        dlg = <BulkDnsDialog ids={modal.ids} data={data} />;
        break;
      case 'bulkList':
        dlg = <BulkListCreateDialog ids={modal.ids} data={data} />;
        break;
      case 'bulkListEdit':
        dlg = <BulkListEditDialog ids={modal.ids} data={data} />;
        break;
      case 'bulkMute':
        dlg = <BulkMuteDialog ids={modal.ids} data={data} />;
        break;
      case 'batch':
        dlg = <BatchDialog mode={modal.mode} data={data} />;
        break;
    }
  }
  return (
    <>
      {drawerRec && <RecordDrawer key={drawerRec.id} r={drawerRec} data={data} />}
      {dlg}
    </>
  );
}

function DeleteDialog({ r, zone }: { r: DNSRecord; zone: string }) {
  const [busy, setBusy] = useState(false);
  const del = async () => {
    setBusy(true);
    if (await act(() => api('DELETE', `/api/records/${r.id}`), '已刪除，可於操作紀錄還原')) {
      closeModal();
      if (drawerStore.get() === r.id) drawerStore.set(null);
    }
    setBusy(false);
  };
  return (
    <Dialog
      title="刪除 Record"
      onClose={closeModal}
      width={500}
      testId="delete-dialog"
      footer={<DialogActions onCancel={closeModal} confirm="刪除" danger disabled={busy} onConfirm={del} cancelFirst />}
    >
      <p>將自 Cloudflare 刪除：</p>
      <p className="mono delete-target">
        {r.type} {r.name} → {r.content}
      </p>
      {(r.meta.owner || r.meta.purpose) && (
        <p className="small">
          負責人：{r.meta.owner || '未指派'}　用途：{r.meta.purpose || '未填寫'}
        </p>
      )}
      {r.listing && <p className="small lv-medium">將一併自入口下架。</p>}
      <p className="small muted">可於操作紀錄還原。{sub(r.name, zone) === '@' ? '此為根網域 record。' : ''}</p>
    </Dialog>
  );
}

type BulkField = 'owner' | 'purpose' | 'expires_at' | 'auto_delete' | 'no_scan';

function BulkMetaDialog({ ids, focusOwner, data }: { ids: string[]; focusOwner?: boolean; data: RecordsData }) {
  const [on, tick] = useOpts<BulkField>({ owner: !!focusOwner, purpose: false, expires_at: false, auto_delete: false, no_scan: false });
  const [v, setV] = useState({ owner: '', purpose: '', expires_at: '', auto_delete: 'on', no_scan: 'on' });
  const ownerRef = useRef<HTMLInputElement>(null);
  const { busy, failed, run } = useBulk(closeModal);
  useEffect(() => {
    if (focusOwner) window.setTimeout(() => ownerRef.current?.focus(), 0);
  }, [focusOwner]);
  const set = (k: BulkField, val: string) => setV((p) => ({ ...p, [k]: val }));
  const save = () => {
    const body: Record<string, unknown> = {};
    if (on.owner) body.owner = v.owner;
    if (on.purpose) body.purpose = v.purpose;
    if (on.expires_at) body.expires_at = v.expires_at;
    if (on.auto_delete) body.auto_delete = v.auto_delete === 'on';
    if (on.no_scan) body.no_scan = v.no_scan === 'off';
    if (!Object.keys(body).length) return toast('請勾選要修改的欄位', { error: true });
    run('/api/records/bulk-meta', { ids, set: body }, (n) => `已更新 ${n} 筆`);
  };
  return (
    <Dialog
      title={`設定管理資訊（${ids.length} 筆）`}
      onClose={closeModal}
      width={560}
      testId="bulk-meta"
      footer={<DialogActions onCancel={closeModal} confirm="儲存" disabled={busy} onConfirm={save} />}
    >
      <FailedList items={failed} />
      <p className="small muted">僅修改勾選的欄位。</p>
      <OptRow {...tick('owner')} label="負責人">
        <SuggestInput
          ref={ownerRef}
          options={ownersOf(data.records)}
          disabled={!on.owner}
          value={v.owner}
          placeholder="姓名或職稱"
          aria-label="負責人"
          onChange={(e) => set('owner', e.target.value)}
        />
      </OptRow>
      <OptRow {...tick('purpose')} label="用途">
        <input className="input" disabled={!on.purpose} value={v.purpose} aria-label="用途" onChange={(e) => set('purpose', e.target.value)} />
      </OptRow>
      <OptRow {...tick('expires_at')} label="到期日" tip="空白代表清除到期日。">
        <div className="row-gap">
          <input className="input date" type="date" disabled={!on.expires_at} value={v.expires_at} aria-label="到期日" onChange={(e) => set('expires_at', e.target.value)} />
          <ExpiryButtons data={data} disabled={!on.expires_at} onPick={(d) => set('expires_at', d)} />
        </div>
      </OptRow>
      <OptRow {...tick('auto_delete')} label="自動刪除">
        <select className="input" disabled={!on.auto_delete} value={v.auto_delete} aria-label="自動刪除" onChange={(e) => set('auto_delete', e.target.value)}>
          <option value="on">啟用</option>
          <option value="off">停用</option>
        </select>
      </OptRow>
      <OptRow {...tick('no_scan')} label="連線檢查">
        <select className="input" disabled={!on.no_scan} value={v.no_scan} aria-label="連線檢查" onChange={(e) => set('no_scan', e.target.value)}>
          <option value="on">啟用</option>
          <option value="off">停用</option>
        </select>
      </OptRow>
    </Dialog>
  );
}

function BulkDeleteDialog({ ids, data }: { ids: string[]; data: RecordsData }) {
  const recs = pickRecords(data, ids);
  const [sure, setSure] = useState(false);
  // Deleting is the one bulk action that clears the selection.
  const { busy, failed, run } = useBulk(() => {
    selectionStore.set(new Set());
    closeModal();
  });
  const del = () => run('/api/records/bulk-delete', { ids }, (n) => `已刪除 ${n} 筆`);
  return (
    <Dialog
      title={`刪除 ${recs.length} 筆 Record`}
      onClose={closeModal}
      width={560}
      testId="bulk-delete"
      footer={<DialogActions onCancel={closeModal} confirm="刪除" danger disabled={!sure || busy} onConfirm={del} cancelFirst />}
    >
      <FailedList items={failed} />
      <ul className="bulk-list mono small">
        {recs.map((r) => (
          <li key={r.id}>
            {r.type} {r.name}
            {r.listing && (
              <span className="lv-medium" data-tip="將一併自入口下架" tabIndex={0}>
                （已上架）
              </span>
            )}
          </li>
        ))}
      </ul>
      <p className="small muted">刪除前將自動保存快照。</p>
      <label className="check">
        <input type="checkbox" checked={sure} onChange={(e) => setSure(e.target.checked)} />
        確認刪除以上 record
      </label>
    </Dialog>
  );
}
