import { useMemo, useRef, useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { semesterEnd } from '../../shared/format';
import { Dialog, Tip, toast } from '../../shared/ui';
import { CSV_TEMPLATE, csvObjects, truthy } from '../lib/csv';
import { refreshRecords } from '../lib/data';
import { REQUEST_TYPES, sub } from '../lib/records';
import { closeModal } from '../lib/store';
import type { Meta, RecordInput, RecordsData } from '../types';

type Row = RecordInput & { meta: Meta };

type Result = { index: number; ok: boolean; name: string; type: string; content: string; error?: string; warning?: string; record_id?: string };

const pad = (n: number, w: number) => String(n).padStart(w, '0');

export function BatchDialog({ mode: initialMode, data }: { mode: 'rule' | 'csv'; data: RecordsData }) {
  const [mode, setMode] = useState(initialMode);
  const [nameTpl, setNameTpl] = useState('student{n}');
  const [type, setType] = useState('A');
  const [contentTpl, setContentTpl] = useState('10.0.3.{n}');
  const [from, setFrom] = useState('1');
  const [to, setTo] = useState('20');
  const [width, setWidth] = useState('3');
  const [csv, setCsv] = useState('');
  const [owner, setOwner] = useState('');
  const [purpose, setPurpose] = useState('');
  const [expires, setExpires] = useState('');
  const [autoDelete, setAutoDelete] = useState(false);
  // A dry run is valid only for the exact rows it checked.
  const [checked, setChecked] = useState<{ rows: Row[]; results: Result[] } | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const rows = useMemo((): Row[] | string => {
    const common = { owner, purpose, notes: '', expires_at: expires, auto_delete: autoDelete, no_scan: false };
    if (mode === 'rule') {
      const a = parseInt(from, 10);
      const b = parseInt(to, 10);
      if (!nameTpl.includes('{n}')) return '名稱範本須包含 {n}';
      if (!Number.isFinite(a) || !Number.isFinite(b) || b < a) return '請填寫正確的起始與結束編號';
      if (b - a + 1 > 500) return '一次最多 500 筆';
      const w = +width;
      const out: Row[] = [];
      for (let n = a; n <= b; n++) {
        out.push({
          type,
          name: nameTpl.replaceAll('{n}', w ? pad(n, w) : String(n)),
          content: contentTpl.replaceAll('{n}', String(n)),
          ttl: 1,
          proxied: false,
          priority: type === 'MX' ? 10 : null,
          comment: '',
          meta: common,
        });
      }
      return out;
    }
    if (!csv.trim()) return '請選擇檔案或貼上 CSV 內容';
    const objs = csvObjects(csv);
    if (!objs.length) return 'CSV 沒有資料列';
    if (!('name' in objs[0]) || !('type' in objs[0]) || !('content' in objs[0])) return 'CSV 第一列須包含 name、type、content 欄位';
    if (objs.length > 500) return '一次最多 500 筆';
    return objs.map((o) => {
      let content = o.content;
      let priority: number | null = null;
      const t = (o.type || 'A').toUpperCase();
      if (t === 'MX') {
        const m = content.match(/^(\d+)\s+(.+)$/);
        if (m) {
          priority = +m[1];
          content = m[2];
        } else priority = 10;
      }
      return {
        type: t,
        name: sub(o.name, data.zone),
        content,
        ttl: +o.ttl || 1,
        proxied: truthy(o.proxied),
        priority,
        comment: o.comment ?? '',
        meta: {
          owner: o.owner || owner,
          purpose: o.purpose || purpose,
          notes: o.notes ?? '',
          expires_at: o.expires_at || expires,
          auto_delete: o.auto_delete ? truthy(o.auto_delete) : autoDelete,
          no_scan: truthy(o.no_scan),
        },
      };
    });
  }, [mode, nameTpl, type, contentTpl, from, to, width, csv, owner, purpose, expires, autoDelete, data.zone]);

  const run = async (dry: boolean) => {
    setError('');
    if (typeof rows === 'string') {
      setError(rows);
      return;
    }
    setBusy(true);
    try {
      const res = await api<{ results: Result[]; created: number }>('POST', '/api/records/batch', { records: rows, dry_run: dry });
      if (dry) setChecked({ rows, results: res.results });
      else {
        toast(`已建立 ${res.created} 筆`);
        closeModal();
        refreshRecords();
      }
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const preview = checked && checked.rows === rows ? checked.results : null;
  const okCount = preview?.filter((r) => r.ok).length ?? 0;
  const skip = (preview?.length ?? 0) - okCount;

  return (
    <Dialog
      title="批次新增"
      onClose={closeModal}
      width={860}
      testId="batch-dialog"
      footer={
        <>
          {error && <span className="lv-high small foot-error">{error}</span>}
          <span className="spacer" />
          <button type="button" className="btn" onClick={closeModal}>
            取消
          </button>
          <button type="button" className="btn" disabled={busy} onClick={() => run(true)}>
            預覽
          </button>
          <button type="button" className="btn primary" disabled={busy || !preview || okCount === 0} onClick={() => run(false)}>
            建立 {preview ? okCount : ''} 筆
          </button>
        </>
      }
    >
      <div className="mode-switch">
        <button type="button" className={'linkbtn' + (mode === 'rule' ? ' current' : ' plain')} aria-pressed={mode === 'rule'} onClick={() => setMode('rule')}>
          依規則產生
        </button>
        <span className="faint">/</span>
        <button type="button" className={'linkbtn' + (mode === 'csv' ? ' current' : ' plain')} aria-pressed={mode === 'csv'} onClick={() => setMode('csv')}>
          CSV
        </button>
      </div>
      {mode === 'rule' ? (
        <div className="batch-rule">
          <div className="field">
            <label htmlFor="b-name">
              名稱範本 <Tip text="{n} 會替換為編號，例：student{n} → student001" />
            </label>
            <div className="suffix-input">
              <input id="b-name" className="input mono" value={nameTpl} onChange={(e) => setNameTpl(e.target.value)} />
              <span className="suffix">.{data.zone}</span>
            </div>
          </div>
          <div className="field">
            <label htmlFor="b-type">Type</label>
            <select id="b-type" className="input" value={type} onChange={(e) => setType(e.target.value)}>
              {REQUEST_TYPES.map((t) => (
                <option key={t}>{t}</option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="b-content">
              內容範本 <Tip text="{n} 替換為編號，不補零，例：10.0.3.{n}" />
            </label>
            <input id="b-content" className="input mono" value={contentTpl} onChange={(e) => setContentTpl(e.target.value)} />
          </div>
          <div className="three">
            <div className="field">
              <label htmlFor="b-from">起始編號</label>
              <input id="b-from" className="input" type="number" value={from} onChange={(e) => setFrom(e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="b-to">結束編號</label>
              <input id="b-to" className="input" type="number" value={to} onChange={(e) => setTo(e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="b-pad">名稱補零</label>
              <select id="b-pad" className="input" value={width} onChange={(e) => setWidth(e.target.value)}>
                <option value="0">不補零</option>
                <option value="2">2 位</option>
                <option value="3">3 位</option>
              </select>
            </div>
          </div>
        </div>
      ) : (
        <div className="field">
          <div className="row-gap">
            <input
              ref={fileRef}
              type="file"
              accept=".csv,text/csv"
              hidden
              onChange={async (e) => {
                const f = e.target.files?.[0];
                if (f) setCsv(await f.text());
              }}
            />
            <button type="button" className="btn small" onClick={() => fileRef.current?.click()}>
              選擇檔案
            </button>
            <a className="btn small" href={'data:text/csv;charset=utf-8,' + encodeURIComponent(CSV_TEMPLATE)} download="records-template.csv">
              下載範本
            </a>
            <Tip text="第一列為欄位名稱，至少需要 name、type、content；也接受交接清單格式。" />
          </div>
          <textarea
            className="input mono csv-text"
            aria-label="CSV 內容"
            value={csv}
            placeholder={'name,type,content,owner\nstudent001,A,10.0.3.1,王小明'}
            onChange={(e) => setCsv(e.target.value)}
          />
        </div>
      )}
      <h3 className="col-title">共同欄位</h3>
      <div className="three">
        <div className="field">
          <label htmlFor="b-owner">負責人</label>
          <input id="b-owner" className="input" value={owner} onChange={(e) => setOwner(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="b-purpose">用途</label>
          <input id="b-purpose" className="input" value={purpose} onChange={(e) => setPurpose(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="b-exp">到期日</label>
          <div className="row-gap">
            <input id="b-exp" className="input date" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            <button type="button" className="btn small" onClick={() => setExpires(semesterEnd(data.today))}>
              學期末
            </button>
          </div>
        </div>
      </div>
      <label className="check">
        <input type="checkbox" checked={autoDelete} onChange={(e) => setAutoDelete(e.target.checked)} />
        到期自動刪除
      </label>
      {mode === 'csv' && <p className="small muted">CSV 中有填寫的欄位優先於共同欄位。</p>}
      {preview && (
        <div className="preview">
          <h3 className="col-title">
            可建立 {okCount} 筆{skip > 0 ? `，${skip} 筆將略過` : ''}
          </h3>
          <div className="preview-scroll">
            <table className="plain small">
              <thead>
                <tr>
                  <th>結果</th>
                  <th>名稱</th>
                  <th>類型</th>
                  <th>內容</th>
                  <th>錯誤或警告</th>
                </tr>
              </thead>
              <tbody>
                {preview.map((r) => (
                  <tr key={r.index}>
                    <td className={r.ok ? 'ok-text' : 'lv-high'}>{r.ok ? '可建立' : '略過'}</td>
                    <td className="mono">{sub(r.name, data.zone)}</td>
                    <td className="mono">{r.type}</td>
                    <td className="mono">{r.content}</td>
                    <td className={r.error ? 'lv-high' : 'lv-medium'}>{r.error || r.warning || ''}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </Dialog>
  );
}
