import { useQuery } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { Tip, toast } from '../../shared/ui';
import { refreshRecords } from '../lib/data';
import type { Settings } from '../types';

const SWATCHES = ['#2dd4bf', '#38bdf8', '#818cf8', '#f472b6', '#fb923c', '#facc15', '#4ade80'];
const KEYS = [
  'title',
  'subtitle',
  'accent',
  'announce_text',
  'announce_level',
  'zombie_days',
  'expiry_warn_days',
  'scan_ports',
  'name_rule_hint',
  'name_rule',
  'requests_enabled',
  'request_note',
] as const;

function MediaField({ kind, label, url, hint, onChange }: { kind: 'logo' | 'hero'; label: string; url: string; hint: string; onChange: (s: Settings) => void }) {
  const ref = useRef<HTMLInputElement>(null);
  const upload = async (f: File) => {
    const fd = new FormData();
    fd.append('file', f);
    try {
      onChange(await api<Settings>('POST', `/api/settings/media/${kind}`, fd));
      toast('已上傳');
    } catch (e) {
      toast(errMsg(e), { error: true });
    }
  };
  const remove = async () => {
    try {
      onChange(await api<Settings>('DELETE', `/api/settings/media/${kind}`));
      toast('已移除');
    } catch (e) {
      toast(errMsg(e), { error: true });
    }
  };
  return (
    <div className="media-field">
      <div className="field-label">
        {label} <Tip text={hint} />
      </div>
      <div className={'media-preview ' + kind}>{url ? <img src={url} alt={label} /> : <span className="muted">無</span>}</div>
      <div className="row-gap">
        <input
          ref={ref}
          type="file"
          hidden
          accept={kind === 'hero' ? 'image/png,image/jpeg,image/gif,image/webp' : 'image/*,.svg,.ico'}
          data-testid={'upload-' + kind}
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) upload(f);
            e.target.value = '';
          }}
        />
        <button type="button" className="btn small" onClick={() => ref.current?.click()}>
          上傳
        </button>
        {url && (
          <button type="button" className="btn small danger-text" onClick={remove}>
            移除
          </button>
        )}
      </div>
    </div>
  );
}

export function SettingsPage() {
  const q = useQuery({ queryKey: ['settings'], queryFn: () => api<Settings>('GET', '/api/settings') });
  const [form, setForm] = useState<Record<string, string>>({});
  const [media, setMedia] = useState({ logo: '', hero: '' });
  const [showRule, setShowRule] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (q.data) {
      const f: Record<string, string> = {};
      KEYS.forEach((k) => (f[k] = q.data[k] ?? ''));
      setForm(f);
      setMedia({ logo: q.data.logo_url, hero: q.data.hero_url });
      setShowRule(!!q.data.name_rule);
    }
  }, [q.data]);
  if (q.error) return <div className="errbox">{errMsg(q.error)}</div>;
  if (!q.data || !form.title) return <p className="muted">讀取中…</p>;
  const set = (k: string) => (e: { target: { value: string } }) => setForm((p) => ({ ...p, [k]: e.target.value }));
  const save = async () => {
    setBusy(true);
    try {
      await api('PUT', '/api/settings', form);
      toast('已儲存');
      q.refetch();
      refreshRecords();
    } catch (e) {
      toast(errMsg(e), { error: true });
    } finally {
      setBusy(false);
    }
  };
  const onMedia = (s: Settings) => setMedia({ logo: s.logo_url, hero: s.hero_url });

  return (
    <form
      className="settings"
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <section className="panel">
        <h2 className="panel-title">品牌</h2>
        <div className="two">
          <div className="field">
            <label htmlFor="st-title">網站名稱</label>
            <input id="st-title" className="input" value={form.title} maxLength={40} onChange={set('title')} />
          </div>
          <div className="field">
            <label htmlFor="st-sub">副標題</label>
            <input id="st-sub" className="input" value={form.subtitle} maxLength={120} placeholder={`${q.data.zone} 服務一覽`} onChange={set('subtitle')} />
          </div>
        </div>
        <div className="field">
          <label htmlFor="st-accent">主色</label>
          <div className="row-gap">
            <input type="color" className="color" aria-label="色盤" value={form.accent} onChange={set('accent')} />
            <input id="st-accent" className="input mono hex" value={form.accent} onChange={set('accent')} />
            {SWATCHES.map((c) => (
              <button
                key={c}
                type="button"
                className={'swatch' + (form.accent.toLowerCase() === c ? ' current' : '')}
                style={{ background: c }}
                aria-label={'主色 ' + c}
                data-tip={c}
                onClick={() => setForm((p) => ({ ...p, accent: c }))}
              />
            ))}
          </div>
        </div>
      </section>

      <section className="panel">
        <h2 className="panel-title">圖片</h2>
        <div className="two">
          <MediaField kind="logo" label="Logo" url={media.logo} hint="顯示於入口頁左上角。建議正方形，PNG 或 SVG，上限 1 MB。" onChange={onMedia} />
          <MediaField kind="hero" label="頁首背景圖" url={media.hero} hint="入口頁頁首的背景，會加上深色遮罩。建議 2400 × 800 px，PNG、JPEG、WebP，上限 5 MB。" onChange={onMedia} />
        </div>
      </section>

      <section className="panel">
        <h2 className="panel-title">公告</h2>
        <div className="field">
          <label htmlFor="st-ann">內容</label>
          <textarea id="st-ann" className="input" value={form.announce_text} placeholder="留空則不顯示" maxLength={300} onChange={set('announce_text')} />
        </div>
        <div className="field">
          <label htmlFor="st-annlv">樣式</label>
          <select id="st-annlv" className="input narrow" value={form.announce_level} onChange={set('announce_level')}>
            <option value="info">一般</option>
            <option value="warn">維護、注意</option>
          </select>
        </div>
      </section>

      <section className="panel">
        <h2 className="panel-title">管理規則</h2>
        <div className="three">
          <div className="field">
            <label htmlFor="st-zombie">
              長期無回應判定（日） <Tip text="連線檢查連續無回應達此天數，列為「長期無回應」。" />
            </label>
            <input id="st-zombie" className="input" type="number" min={1} max={365} value={form.zombie_days} onChange={set('zombie_days')} />
          </div>
          <div className="field">
            <label htmlFor="st-warn">
              到期提醒（日前） <Tip text="到期日在此天數內時列為「即將到期」。" />
            </label>
            <input id="st-warn" className="input" type="number" min={1} max={365} value={form.expiry_warn_days} onChange={set('expiry_warn_days')} />
          </div>
          <div className="field">
            <label htmlFor="st-ports">
              檢查連接埠 <Tip text="連線檢查時嘗試的 TCP 連接埠，以逗號分隔，任一可連線即視為正常。" />
            </label>
            <input id="st-ports" className="input mono" value={form.scan_ports} onChange={set('scan_ports')} />
          </div>
        </div>
        <div className="field">
          <label htmlFor="st-hint">
            命名規則說明 <Tip text="顯示於新增 Record 與申請頁的名稱欄位下方。" />
          </label>
          <input id="st-hint" className="input" value={form.name_rule_hint} onChange={set('name_rule_hint')} />
        </div>
        <button type="button" className="linkbtn" aria-expanded={showRule} onClick={() => setShowRule((v) => !v)}>
          {showRule ? '收合自訂命名規則' : '自訂命名規則（進階）'}
        </button>
        {showRule && (
          <div className="field rule-field">
            <label htmlFor="st-rule">
              自訂命名規則（正規表示式） <Tip text="需完全符合整個子網域名稱，例：(hpc|course)-[a-z0-9-]+。含 _ 開頭段落的名稱不套用。留空則只檢查基本格式。" />
            </label>
            <input id="st-rule" className="input mono" value={form.name_rule} onChange={set('name_rule')} />
          </div>
        )}
      </section>

      <section className="panel">
        <h2 className="panel-title">申請網域</h2>
        <label className="check">
          <input type="checkbox" checked={form.requests_enabled === '1'} onChange={(e) => setForm((p) => ({ ...p, requests_enabled: e.target.checked ? '1' : '0' }))} />
          開放申請
        </label>
        <div className="field top-gap">
          <label htmlFor="st-rnote">申請頁說明</label>
          <textarea id="st-rnote" className="input" value={form.request_note} maxLength={1000} onChange={set('request_note')} />
        </div>
      </section>

      <div className="save-bar">
        <button type="submit" className="btn primary" disabled={busy}>
          儲存設定
        </button>
      </div>
    </form>
  );
}
