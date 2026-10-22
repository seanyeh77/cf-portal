import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { api, errMsg } from '../../shared/api';
import { addMonth, semesterEnd } from '../../shared/format';
import { confirmDialog, Dialog, DialogActions, Tip, toast } from '../../shared/ui';
import { refreshRecords } from '../lib/data';
import {
  dependents,
  fqdn,
  isPublicIP,
  proxyWarning,
  nameRuleError,
  normTarget,
  PROXIABLE,
  SCANNABLE,
  sub,
  TTL_OPTIONS,
  ttlLabel,
  DNS_FIELD_LABEL,
  fieldValue,
  ownersOf,
} from '../lib/records';
import { closeModal, type EditorFocus } from '../lib/store';
import type { DNSRecord, RecordsData } from '../types';

const TYPE_HELP: Record<string, string> = {
  A: 'A：指向 IPv4 位址，適用於多數自架服務。',
  AAAA: 'AAAA：指向 IPv6 位址。',
  CNAME: 'CNAME：指向另一個網址，例如 GitHub Pages。同名不可再有 A、AAAA、CNAME。',
  TXT: 'TXT：文字資料，用於網域驗證或郵件設定（SPF、DMARC）。',
  MX: 'MX：郵件伺服器，須設定 Priority，數字越小越優先。',
};

const CONTENT: Record<string, [string, string]> = {
  A: ['IPv4 位址', '例：10.0.1.20'],
  AAAA: ['IPv6 位址', '例：2001:db8::20'],
  CNAME: ['目標網址', '例：my-site.github.io'],
  TXT: ['文字內容', '例：v=spf1 -all'],
  MX: ['郵件伺服器', '例：mail.example.com'],
};

type DNSForm = { type: string; name: string; content: string; ttl: number; proxied: boolean };

/** Fields whose change needs the confirmation dialog. */
const CONFIRM_FIELDS: (keyof DNSForm)[] = ['type', 'name', 'content', 'proxied', 'ttl'];

export function RecordEditor({ record, data, focus }: { record?: DNSRecord; data: RecordsData; focus?: EditorFocus }) {
  const zone = data.zone;
  const isNew = !record;
  const [type, setType] = useState(record?.type ?? 'A');
  const [name, setName] = useState(record ? sub(record.name, zone) : '');
  const [content, setContent] = useState(record?.content ?? '');
  const [ttl, setTtl] = useState(record?.ttl ?? 1);
  const [proxied, setProxied] = useState(record?.proxied ?? false);
  const [priority, setPriority] = useState(record?.priority != null ? String(record.priority) : '10');
  const [comment, setComment] = useState(record?.comment ?? '');
  const [owner, setOwner] = useState(record?.meta.owner ?? '');
  const [purpose, setPurpose] = useState(record?.meta.purpose ?? '');
  const [notes, setNotes] = useState(record?.meta.notes ?? '');
  const [expires, setExpires] = useState(record?.meta.expires_at ?? '');
  const [autoDelete, setAutoDelete] = useState(record?.meta.auto_delete ?? false);
  const [noScan, setNoScan] = useState(record?.meta.no_scan ?? false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const refs = {
    content: useRef<HTMLInputElement>(null),
    proxied: useRef<HTMLInputElement>(null),
    name: useRef<HTMLInputElement>(null),
    expires: useRef<HTMLInputElement>(null),
    owner: useRef<HTMLInputElement>(null),
  };
  useEffect(() => {
    const el = focus ? refs[focus].current : refs.name.current;
    if (!el) return;
    window.setTimeout(() => {
      el.focus();
      if (focus === 'content') el.select();
    }, 0);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const canProxy = PROXIABLE.has(type);
  const fullName = fqdn(name, zone);
  const nameChanged = !record || normTarget(record.name) !== fullName;
  const ruleErr = nameRuleError(name, zone, data.settings.name_rule);
  const others = data.records.filter((r) => r.id !== record?.id && normTarget(r.name) === fullName);
  const owners = useMemo(() => ownersOf(data.records), [data.records]);

  const warnings: string[] = [];
  // A CNAME cannot share its name with A, AAAA or CNAME (the scannable set).
  const cnameClash = others.find((o) => (type === 'CNAME' && SCANNABLE.has(o.type)) || (o.type === 'CNAME' && SCANNABLE.has(type)));
  if (cnameClash) warnings.push(`${fullName} 已有 ${cnameClash.type} record，CNAME 不可與其他 record 同名。`);
  const sameType = others.filter((o) => o.type === type);
  if (sameType.length && !cnameClash) {
    const identical = sameType.some((o) => normTarget(o.content) === normTarget(content));
    warnings.push(identical ? `已有相同的 ${type} record。` : `${fullName} 已有 ${sameType.length} 筆 ${type} record，儲存後會同時存在。`);
  }
  const proxyWarn = proxyWarning(type, proxied, content);
  if (proxyWarn) warnings.push(proxyWarn);

  const form: DNSForm = { type, name: fullName, content: content.trim(), ttl: proxied && canProxy ? 1 : ttl, proxied: proxied && canProxy };

  const save = async () => {
    setError('');
    if (autoDelete && !expires) {
      setError('自動刪除需設定到期日');
      toast('自動刪除需設定到期日', { error: true });
      refs.expires.current?.focus();
      return;
    }
    if (nameChanged && ruleErr) {
      setError('名稱格式不符：' + ruleErr);
      refs.name.current?.focus();
      return;
    }
    const meta = { owner, purpose, notes, expires_at: expires, auto_delete: autoDelete, no_scan: noScan };
    const body = {
      ...form,
      priority: type === 'MX' ? Number(priority) : null,
      comment,
      meta,
    };
    if (record) {
      const before: DNSForm = { type: record.type, name: normTarget(record.name), content: record.content, ttl: record.ttl, proxied: record.proxied };
      const changes = CONFIRM_FIELDS.filter((k) =>
        k === 'name' ? before.name !== form.name : k === 'content' ? normTarget(String(before.content)) !== normTarget(form.content) : before[k] !== form[k],
      );
      if (changes.length > 0) {
        const reasons: string[] = [];
        if (before.proxied && !form.proxied && ['A', 'AAAA'].includes(form.type) && isPublicIP(form.content)) reasons.push('停用 Proxy 後，來源主機的 IP 會公開。');
        if (changes.includes('type') || changes.includes('name')) reasons.push('變更類型或名稱等同刪除舊 record 再新增，舊名稱將立即失效。');
        const deps = dependents(data.records, record.name).filter((d) => d.id !== record.id);
        if (deps.length && (changes.includes('name') || changes.includes('content'))) reasons.push(`另有 ${deps.length} 筆 CNAME 指向此 record，會一併受影響。`);
        if (record.listing) reasons.push('此 record 已上架到服務入口。');
        const risky = reasons.length > 0;
        const ok = await confirmDialog({
          title: risky ? '確認高風險 DNS 變更' : '確認 DNS 變更',
          confirmText: risky ? '確認變更' : '儲存',
          danger: risky,
          width: 560,
          body: (
            <DiffTable reasons={reasons} rows={changes.map((k) => [DNS_FIELD_LABEL[k], fieldValue(k, before[k]), fieldValue(k, form[k])])} />
          ),
        });
        if (!ok) return;
      }
    }
    setBusy(true);
    try {
      // The server reports whether it queued a connection check.
      const res = isNew
        ? await api<{ scanning: boolean }>('POST', '/api/records', body)
        : await api<{ scanning: boolean }>('PATCH', `/api/records/${record!.id}`, body);
      toast((isNew ? '已新增' : '已儲存') + (res.scanning ? '，連線檢查中' : ''));
      closeModal();
      await refreshRecords();
    } catch (e) {
      setError(errMsg(e));
      toast(errMsg(e), { error: true });
    } finally {
      setBusy(false);
    }
  };

  const [cLabel, cPlaceholder] = CONTENT[type] ?? ['內容', ''];

  return (
    <Dialog
      title={isNew ? '新增 Record' : `編輯 ${record!.type} ${sub(record!.name, zone)}`}
      onClose={closeModal}
      width={880}
      className="editor"
      testId="record-editor"
      footer={<DialogActions error={error} onCancel={closeModal} confirm={isNew ? '新增' : '儲存'} disabled={busy} onConfirm={save} />}
    >
      <form
        className="editor-grid"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <section>
          <h3 className="col-title">DNS 設定</h3>
          {warnings.length > 0 && (
            <div className="warnbox" role="alert">
              <ul>
                {warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </div>
          )}
          <div className="two">
            <div className="field">
              <label htmlFor="ed-type">
                Type <Tip text={TYPE_HELP[type] ?? ''} />
              </label>
              <select id="ed-type" className="input" value={type} onChange={(e) => setType(e.target.value)}>
                {Object.keys(TYPE_HELP).map((t) => (
                  <option key={t}>{t}</option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="ed-ttl">TTL</label>
              <select id="ed-ttl" className="input" value={form.ttl} disabled={form.proxied} onChange={(e) => setTtl(+e.target.value)}>
                {TTL_OPTIONS.map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
                {!TTL_OPTIONS.some(([v]) => v === ttl) && <option value={ttl}>{ttlLabel(ttl)}</option>}
              </select>
            </div>
          </div>
          <div className="field">
            <label htmlFor="ed-name">名稱</label>
            <div className={'suffix-input' + (nameChanged && ruleErr ? ' invalid' : '')}>
              <input
                ref={refs.name}
                id="ed-name"
                className="input"
                value={name}
                placeholder="例：grafana；根網域填 @"
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setName(e.target.value)}
              />
              <span className="suffix">.{zone}</span>
            </div>
            <span className={'hint' + (ruleErr ? (nameChanged ? ' error' : '') : '')}>
              {ruleErr ? (nameChanged ? ruleErr : `${ruleErr}（既有名稱，可保留）`) : data.settings.name_rule_hint}
            </span>
          </div>
          <div className="field">
            <label htmlFor="ed-content">{cLabel}</label>
            <input
              ref={refs.content}
              id="ed-content"
              className="input mono"
              value={content}
              placeholder={cPlaceholder}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setContent(e.target.value)}
            />
          </div>
          {type === 'MX' && (
            <div className="field">
              <label htmlFor="ed-prio">Priority</label>
              <input id="ed-prio" className="input" type="number" min={0} max={65535} value={priority} onChange={(e) => setPriority(e.target.value)} />
            </div>
          )}
          <div className="field">
            <span className="field-label">
              <label className="check">
                <input
                  ref={refs.proxied}
                  type="checkbox"
                  checked={proxied && canProxy}
                  disabled={!canProxy}
                  onChange={(e) => setProxied(e.target.checked)}
                />
                Cloudflare Proxy
              </label>
              <Tip text="經 Cloudflare 轉送並隱藏來源 IP。內網位址不可啟用。" />
            </span>
          </div>
          <div className="field">
            <label htmlFor="ed-comment">Cloudflare 備註</label>
            <input id="ed-comment" className="input" value={comment} placeholder="選填" maxLength={100} onChange={(e) => setComment(e.target.value)} />
          </div>
        </section>
        <section className="meta-col">
          <h3 className="col-title">
            管理資訊 <Tip text="僅儲存於本系統，不寫入 Cloudflare" />
          </h3>
          <div className="field">
            <label htmlFor="ed-owner">負責人</label>
            <input
              ref={refs.owner}
              id="ed-owner"
              className="input"
              list="owner-list"
              value={owner}
              placeholder="姓名或職稱"
              onChange={(e) => setOwner(e.target.value)}
            />
            <datalist id="owner-list">
              {owners.map((o) => (
                <option key={o} value={o} />
              ))}
            </datalist>
          </div>
          <div className="field">
            <label htmlFor="ed-purpose">用途</label>
            <input id="ed-purpose" className="input" value={purpose} onChange={(e) => setPurpose(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="ed-notes">備註</label>
            <textarea id="ed-notes" className="input" value={notes} placeholder="主機位置、登入方式、注意事項" onChange={(e) => setNotes(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="ed-exp">到期日</label>
            <div className="row-gap">
              <input ref={refs.expires} id="ed-exp" className="input date" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
              <button type="button" className="btn small" onClick={() => setExpires(addMonth(data.today))}>
                一個月
              </button>
              <button type="button" className="btn small" onClick={() => setExpires(semesterEnd(data.today))}>
                學期末
              </button>
              <button type="button" className="btn small" onClick={() => setExpires('')}>
                清除
              </button>
            </div>
          </div>
          <div className="field">
            <span className="field-label">
              <label className="check">
                <input type="checkbox" checked={autoDelete} onChange={(e) => setAutoDelete(e.target.checked)} />
                到期自動刪除
              </label>
              <Tip text="超過到期日後由系統自動自 Cloudflare 刪除，並記錄於操作紀錄，可重新建立。" />
            </span>
          </div>
          <div className="field">
            <span className="field-label">
              <label className="check">
                <input type="checkbox" checked={noScan} onChange={(e) => setNoScan(e.target.checked)} />
                停用連線檢查
              </label>
              <Tip text="適用於非常態開機的主機。停用後不再列為長期無回應。" />
            </span>
          </div>
        </section>
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function DiffTable({ rows, reasons }: { rows: [string, string, string][]; reasons: string[] }): ReactNode {
  const risky = reasons.length > 0;
  return (
    <>
      {reasons.length > 0 && (
        <div className="warnbox">
          <ul>
            {reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        </div>
      )}
      <table className="plain diff">
        <thead>
          <tr>
            <th>欄位</th>
            <th>變更</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(([f, a, b]) => (
            <tr key={f}>
              <td>{f}</td>
              <td className="mono">
                <del className="old">{a}</del> → <strong className={risky ? 'lv-high' : undefined}>{b}</strong>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
