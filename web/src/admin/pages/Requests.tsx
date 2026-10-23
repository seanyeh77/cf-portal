import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { fmtDateTimeShort } from '../../shared/format';
import { Dialog, DialogActions, Tip, toast, TriCheck } from '../../shared/ui';
import { FailedList, runBulk, type BulkItem } from '../lib/bulk';
import { refreshRecords, useRecords } from '../lib/data';
import { PROXIABLE, proxyWarning, REQUEST_TYPES, sub } from '../lib/records';
import type { RecordsData, RequestItem } from '../types';

type Resp = { requests: RequestItem[]; enabled: boolean };

export function Requests() {
  const q = useQuery({ queryKey: ['requests'], queryFn: () => api<Resp>('GET', '/api/requests') });
  const { data } = useRecords();
  const [approve, setApprove] = useState<RequestItem | null>(null);
  const [reject, setReject] = useState<RequestItem | null>(null);
  const [sel, setSel] = useState<Set<number>>(new Set());
  const [bulk, setBulk] = useState<'approve' | 'reject' | null>(null);
  if (q.error) return <div className="errbox">{errMsg(q.error)}</div>;
  if (!q.data || !data) return <p className="muted">讀取中…</p>;
  const pending = q.data.requests.filter((r) => r.status === 'pending');
  const done = q.data.requests.filter((r) => r.status !== 'pending');
  // Decided requests drop out of the selection.
  const chosen = pending.filter((r) => sel.has(r.id));
  const toggle = (id: number, on: boolean) =>
    setSel((p) => {
      const n = new Set(p);
      if (on) n.add(id);
      else n.delete(id);
      return n;
    });
  return (
    <div className="requests">
      <div className="page-head">
        <h1>
          網域申請 <Tip text="使用者可於申請頁送出子網域申請，核准後自動建立 record，並帶入負責人、用途與到期日。申請人可於進度頁查看結果與管理者回覆。" />
        </h1>
        <a href="/request" target="_blank" rel="noreferrer">
          開啟申請頁 ↗
        </a>
        {!q.data.enabled && <span className="small muted">目前未開放申請（可於網站設定啟用）</span>}
      </div>
      <h2 className="sec-title req-sel">
        <TriCheck total={pending.length} selected={chosen.length} label="選取全部待審申請" onChange={(all) => setSel(new Set(all ? pending.map((r) => r.id) : []))} />
        待審（{pending.length}）
      </h2>
      {chosen.length > 0 && (
        <div className="bulkbar" role="region" aria-label="批次審核">
          <span>已選 {chosen.length} 筆</span>
          <button type="button" className="linkbtn" onClick={() => setBulk('approve')}>
            核准…
          </button>
          <button type="button" className="linkbtn" onClick={() => setBulk('reject')}>
            駁回…
          </button>
          <span className="spacer" />
          <button type="button" className="linkbtn" onClick={() => setSel(new Set())}>
            取消選取
          </button>
        </div>
      )}
      {pending.length === 0 && <p className="muted small">目前沒有待審申請。</p>}
      {pending.map((r) => (
        <div key={r.id} className="req-card pending" data-testid={'req-' + r.id}>
          <div className="req-head">
            <label className="check-hit" aria-label={'選取 ' + r.name}>
              <input type="checkbox" checked={sel.has(r.id)} onChange={(e) => toggle(r.id, e.target.checked)} />
            </label>
            <strong className="mono">{sub(r.name, data.zone)}</strong>
            <span className="mono small">
              {r.type} → {r.content}
            </span>
            <span className="lv-medium small">待審</span>
            <span className="spacer" />
            <span className="small muted">{fmtDateTimeShort(r.ts)}</span>
          </div>
          <div className="req-body small">
            <div>
              {r.requester}（{r.contact}）
            </div>
            <div>用途：{r.purpose}</div>
            <div>使用期限：{r.expires_at || '未指定'}</div>
            {r.conflict && <div className="lv-medium">注意：{r.conflict}</div>}
            {!r.name_ok && <div className="lv-medium">注意：名稱不符規則（{r.name_error}）</div>}
          </div>
          <div className="row-gap">
            <button type="button" className="btn primary small" onClick={() => setApprove(r)}>
              核准
            </button>
            <button type="button" className="btn small" onClick={() => setReject(r)}>
              駁回
            </button>
          </div>
        </div>
      ))}
      <h2 className="sec-title">已處理</h2>
      {done.length === 0 && <p className="muted small">尚無已處理的申請。</p>}
      {done.map((r) => (
        <div key={r.id} className={'req-card ' + r.status}>
          <div className="req-head">
            <strong className="mono">{sub(r.name, data.zone)}</strong>
            <span className="mono small">
              {r.type} → {r.content}
            </span>
            <span className={'small ' + (r.status === 'approved' ? 'ok-text' : 'muted')}>{r.status === 'approved' ? '已核准' : '已駁回'}</span>
            <span className="spacer" />
            <span className="small muted">{r.decided_ts ? fmtDateTimeShort(r.decided_ts) : ''}</span>
          </div>
          <div className="req-body small">
            <div>
              {r.requester}（{r.contact}）· {r.purpose}
            </div>
            {r.admin_note && <div>管理者備註：{r.admin_note}</div>}
          </div>
        </div>
      ))}
      {approve && <ApproveDialog req={approve} data={data} onClose={() => setApprove(null)} />}
      {reject && <RejectDialog req={reject} onClose={() => setReject(null)} />}
      {bulk === 'approve' && <BulkApproveDialog reqs={chosen} data={data} onClose={() => setBulk(null)} />}
      {bulk === 'reject' && <BulkRejectDialog reqs={chosen} onClose={() => setBulk(null)} />}
    </div>
  );
}

function ApproveDialog({ req, data, onClose }: { req: RequestItem; data: RecordsData; onClose: () => void }) {
  const [type, setType] = useState(req.type);
  const [proxied, setProxied] = useState(false);
  const [name, setName] = useState(sub(req.name, data.zone));
  const [content, setContent] = useState(req.content);
  const [owner, setOwner] = useState(`${req.requester}（${req.contact}）`);
  const [purpose, setPurpose] = useState(req.purpose);
  const [expires, setExpires] = useState(req.expires_at);
  const [note, setNote] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      await api('POST', `/api/requests/${req.id}/approve`, {
        record: { type, name, content, ttl: 1, proxied: proxied && PROXIABLE.has(type), priority: null, comment: '' },
        meta: { owner, purpose, notes: '', expires_at: expires, auto_delete: false, no_scan: false },
        note,
      });
      toast('已核准，連線檢查中');
      onClose();
      refreshRecords();
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      title="核准申請"
      onClose={onClose}
      width={620}
      testId="approve-dialog"
      footer={<DialogActions error={error} onCancel={onClose} confirm="核准並建立" disabled={busy} onConfirm={submit} />}
    >
      {req.conflict && <div className="warnbox">{req.conflict}</div>}
      {proxyWarning(type, proxied, content) && <div className="warnbox">{proxyWarning(type, proxied, content)}</div>}
      <div className="two">
        <div className="field">
          <label htmlFor="ap-type">Type</label>
          <select id="ap-type" className="input" value={type} onChange={(e) => setType(e.target.value)}>
            {REQUEST_TYPES.map((t) => (
              <option key={t}>{t}</option>
            ))}
          </select>
        </div>
        <div className="field">
          <span className="field-label">&nbsp;</span>
          <label className="check">
            <input type="checkbox" checked={proxied && PROXIABLE.has(type)} disabled={!PROXIABLE.has(type)} onChange={(e) => setProxied(e.target.checked)} />
            Cloudflare Proxy
          </label>
        </div>
      </div>
      <div className="field">
        <label htmlFor="ap-name">名稱</label>
        <div className="suffix-input">
          <input id="ap-name" className="input" value={name} onChange={(e) => setName(e.target.value)} />
          <span className="suffix">.{data.zone}</span>
        </div>
      </div>
      <div className="field">
        <label htmlFor="ap-content">內容</label>
        <input id="ap-content" className="input mono" value={content} onChange={(e) => setContent(e.target.value)} />
      </div>
      <div className="two">
        <div className="field">
          <label htmlFor="ap-owner">負責人</label>
          <input id="ap-owner" className="input" value={owner} onChange={(e) => setOwner(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ap-exp">到期日</label>
          <input id="ap-exp" className="input" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
        </div>
      </div>
      <div className="field">
        <label htmlFor="ap-purpose">用途</label>
        <input id="ap-purpose" className="input" value={purpose} onChange={(e) => setPurpose(e.target.value)} />
      </div>
      <div className="field">
        <label htmlFor="ap-note">回覆申請人（選填）</label>
        <textarea id="ap-note" className="input" value={note} onChange={(e) => setNote(e.target.value)} />
      </div>
    </Dialog>
  );
}

function RejectDialog({ req, onClose }: { req: RequestItem; onClose: () => void }) {
  const [note, setNote] = useState('');
  const [error, setError] = useState('');
  const submit = async () => {
    if (!note.trim()) {
      setError('請填寫駁回原因');
      return;
    }
    try {
      await api('POST', `/api/requests/${req.id}/reject`, { note });
      toast('已駁回');
      onClose();
      refreshRecords();
    } catch (e) {
      setError(errMsg(e));
    }
  };
  return (
    <Dialog
      title={`駁回申請：${req.name}`}
      onClose={onClose}
      width={520}
      testId="reject-dialog"
      footer={<DialogActions error={error} onCancel={onClose} confirm="駁回" danger onConfirm={submit} />}
    >
      <div className="field">
        <label htmlFor="rj-note">原因（申請人可見）</label>
        <textarea id="rj-note" className="input" value={note} data-autofocus onChange={(e) => setNote(e.target.value)} />
      </div>
    </Dialog>
  );
}

function BulkApproveDialog({ reqs, data, onClose }: { reqs: RequestItem[]; data: RecordsData; onClose: () => void }) {
  // Requests with a conflict or a name outside the rule start unticked.
  const [ids, setIds] = useState(() => new Set(reqs.filter((r) => !r.conflict && r.name_ok).map((r) => r.id)));
  const [proxied, setProxied] = useState(false);
  const [note, setNote] = useState('');
  const [failed, setFailed] = useState<BulkItem[]>([]);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    const bad = await runBulk('/api/requests/bulk-approve', { ids: [...ids], proxied, note }, (n) => `已核准 ${n} 筆，連線檢查中`);
    setBusy(false);
    if (bad?.length) setFailed(bad);
    else if (bad) onClose();
  };
  return (
    <Dialog
      title={`核准申請（${ids.size} 筆）`}
      onClose={onClose}
      width={640}
      testId="bulk-approve"
      footer={<DialogActions onCancel={onClose} confirm={`核准並建立 ${ids.size} 筆`} disabled={busy || !ids.size} onConfirm={submit} />}
    >
      <FailedList items={failed} />
      <p className="small muted">依各申請的內容建立 record，負責人、用途與到期日帶入申請資料。需要修改內容的申請請改用單筆核准。</p>
      <ul className="bulk-list plain-list">
        {reqs.map((r) => (
          <li key={r.id}>
            <label className="check">
              <input
                type="checkbox"
                checked={ids.has(r.id)}
                onChange={(e) =>
                  setIds((p) => {
                    const n = new Set(p);
                    if (e.target.checked) n.add(r.id);
                    else n.delete(r.id);
                    return n;
                  })
                }
              />
              <span className="mono">{sub(r.name, data.zone)}</span>
              <span className="mono small muted">
                {r.type} → {r.content}
              </span>
            </label>
            {r.conflict && <div className="small lv-medium">注意：{r.conflict}</div>}
            {!r.name_ok && <div className="small lv-medium">注意：名稱不符規則（{r.name_error}）</div>}
          </li>
        ))}
      </ul>
      <label className="check">
        <input type="checkbox" checked={proxied} onChange={(e) => setProxied(e.target.checked)} />
        啟用 Proxy <Tip text="只套用於 A、AAAA、CNAME；內網位址的申請請勿啟用。" />
      </label>
      <div className="field top-gap">
        <label htmlFor="ba-note">給申請人的備註（選填）</label>
        <textarea id="ba-note" className="input" value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
      </div>
    </Dialog>
  );
}

function BulkRejectDialog({ reqs, onClose }: { reqs: RequestItem[]; onClose: () => void }) {
  const [note, setNote] = useState('');
  const [error, setError] = useState('');
  const [failed, setFailed] = useState<BulkItem[]>([]);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!note.trim()) return setError('請填寫駁回原因');
    setBusy(true);
    const bad = await runBulk('/api/requests/bulk-reject', { ids: reqs.map((r) => r.id), note }, (n) => `已駁回 ${n} 筆`);
    setBusy(false);
    if (bad?.length) setFailed(bad);
    else if (bad) onClose();
  };
  return (
    <Dialog
      title={`駁回申請（${reqs.length} 筆）`}
      onClose={onClose}
      width={520}
      testId="bulk-reject"
      footer={<DialogActions error={error} onCancel={onClose} confirm={`駁回 ${reqs.length} 筆`} danger disabled={busy} onConfirm={submit} />}
    >
      <FailedList items={failed} />
      <ul className="bulk-list small">
        {reqs.map((r) => (
          <li key={r.id} className="mono">
            {r.name}
          </li>
        ))}
      </ul>
      <div className="field">
        <label htmlFor="br-note">原因（申請人可見）</label>
        <textarea id="br-note" className="input" value={note} data-autofocus onChange={(e) => setNote(e.target.value)} />
      </div>
    </Dialog>
  );
}
