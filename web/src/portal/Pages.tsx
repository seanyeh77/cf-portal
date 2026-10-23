import { useState, type CSSProperties, type ReactNode } from 'react';
import { fmtDateTimeShort } from '../shared/format';
import { copyText, Tip } from '../shared/ui';
import { inkFor, type Site } from './Portal';

export type PublicInit = {
  site: Site;
  note?: string;
  name_rule_hint?: string;
  errors?: string[] | null;
  values?: Record<string, string>;
  today?: string;
  error?: string;
  username?: string;
  request?: {
    name: string;
    type: string;
    content: string;
    purpose: string;
    expires_at: string;
    requester: string;
    status: 'pending' | 'approved' | 'rejected';
    admin_note: string;
    ts: number;
    decided_ts: number | null;
  } | null;
};

function Shell({ site, children, narrow }: { site: Site; children: ReactNode; narrow?: boolean }) {
  const accent = site?.accent || '#2dd4bf';
  return (
    <div className="simple" style={{ '--site-accent': accent } as CSSProperties}>
      <header className="simple-head">
        <a className="site" href="/">
          {site?.logo ? (
            <img className="logo small" src={site.logo} alt="" />
          ) : (
            <span className="logo mark small" style={{ background: accent, color: inkFor(accent) }}>
              {[...(site?.title || 'S')][0]}
            </span>
          )}
          <span className="site-name">{site?.title}</span>
        </a>
      </header>
      <main className={'simple-main' + (narrow ? ' narrow' : '')}>{children}</main>
    </div>
  );
}

const TYPES = [
  { v: 'A', title: 'IPv4 位址', sub: 'A record', label: 'IPv4 位址', ph: '例：140.113.1.10' },
  { v: 'CNAME', title: '其他網址', sub: 'CNAME，例：GitHub Pages', label: '目標網址', ph: '例：my-name.github.io' },
  { v: 'AAAA', title: 'IPv6 位址', sub: 'AAAA record', label: 'IPv6 位址', ph: '例：2001:db8::10' },
  { v: 'TXT', title: '驗證文字', sub: 'TXT，用於網域驗證', label: '驗證文字', ph: '例：google-site-verification=…' },
];

export function RequestPage({ init }: { init: PublicInit }) {
  const v = init.values ?? {};
  const [type, setType] = useState(v.type || 'A');
  const t = TYPES.find((x) => x.v === type) ?? TYPES[0];
  const errors = init.errors ?? [];
  return (
    <Shell site={init.site}>
      <a className="back" href="/">
        ← 服務入口
      </a>
      <h1 className="page-h">申請網域</h1>
      <p className="lead">
        申請 <span className="mono">*.{init.site.zone}</span> 網址，經核准後建立。
      </p>
      {init.note && <p className="note pre">{init.note}</p>}
      {errors.length > 0 && (
        <div className="errbox" role="alert">
          <ul>
            {errors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </div>
      )}
      <form method="post" action="/request" className="req-form">
        <div className="two-col">
          <div className="field">
            <label htmlFor="rq-name">姓名</label>
            <input id="rq-name" name="requester" className="input" required maxLength={60} defaultValue={v.requester} autoComplete="name" />
          </div>
          <div className="field">
            <label htmlFor="rq-contact">聯絡方式</label>
            <input id="rq-contact" name="contact" className="input" required maxLength={120} placeholder="Email 或 Discord" defaultValue={v.contact} />
          </div>
        </div>
        <div className="field">
          <label htmlFor="rq-sub">
            網址名稱 <Tip text={init.name_rule_hint || '限小寫英文、數字與連字號'} />
          </label>
          <div className="suffix-input">
            <input id="rq-sub" name="name" className="input" required defaultValue={v.name ? v.name.replace(new RegExp(`\\.${init.site.zone.replace(/\./g, '\\.')}$`), '') : ''} autoComplete="off" spellCheck={false} />
            <span className="suffix">.{init.site.zone}</span>
          </div>
        </div>
        <fieldset className="field type-pick">
          <legend className="field-label">指向類型</legend>
          <div className="type-grid">
            {TYPES.map((x) => (
              <label key={x.v} className={'type-box' + (type === x.v ? ' selected' : '')}>
                <input type="radio" name="type" value={x.v} checked={type === x.v} onChange={() => setType(x.v)} />
                <span className="type-title">{x.title}</span>
                <span className="type-sub">{x.sub}</span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="field">
          <label htmlFor="rq-content">{t.label}</label>
          <input id="rq-content" name="content" className="input mono" required placeholder={t.ph} defaultValue={v.content} spellCheck={false} />
        </div>
        <div className="field">
          <label htmlFor="rq-purpose">用途</label>
          <textarea id="rq-purpose" name="purpose" className="input" required maxLength={300} defaultValue={v.purpose} />
        </div>
        <div className="field">
          <label htmlFor="rq-exp">
            使用期限（選填） <Tip text="短期活動或測試請填寫，到期後收回" />
          </label>
          <input id="rq-exp" name="expires_at" className="input" type="date" min={init.today} defaultValue={v.expires_at} />
        </div>
        <button type="submit" className="btn primary block submit">
          送出
        </button>
      </form>
    </Shell>
  );
}

const STATUS: Record<string, [string, string]> = {
  pending: ['審核中', 'lv-medium'],
  approved: ['已核准', 'accent-text'],
  rejected: ['已駁回', 'lv-high'],
};

export function RequestStatusPage({ init }: { init: PublicInit }) {
  const r = init.request;
  if (!r)
    return (
      <Shell site={init.site} narrow>
        <h1 className="page-h">找不到申請</h1>
        <p className="lead">連結可能有誤，請確認網址。</p>
        <a href="/">回到服務入口</a>
      </Shell>
    );
  const [label, cls] = STATUS[r.status];
  const link = window.location.href;
  return (
    <Shell site={init.site} narrow>
      <a className="back" href="/">
        ← 服務入口
      </a>
      <div className={'status-word ' + cls} data-testid="request-status">
        {label}
      </div>
      <h1 className="page-h mono">{r.name}</h1>
      {r.status === 'pending' && (
        <div className="status-note">
          <p>審核結果將顯示於此頁，請保存連結。</p>
          <div className="copy-row">
            <a href={link} className="mono wrap-any">
              {link}
            </a>
            <button
              type="button"
              className="btn small"
              onClick={() => copyText(link, '已複製連結')}
            >
              複製
            </button>
          </div>
        </div>
      )}
      {r.status === 'approved' && (
        <p className="status-note">
          已建立，約數分鐘後生效。
          {r.type !== 'TXT' && (
            <>
              {' '}
              <a href={`https://${r.name}`} target="_blank" rel="noreferrer" className="mono">
                https://{r.name}
              </a>
            </>
          )}
        </p>
      )}
      {r.admin_note && (
        <div className="reply">
          <div className="small muted">管理者回覆</div>
          <p className="pre">{r.admin_note}</p>
        </div>
      )}
      <dl className="detail">
        <dt>類型</dt>
        <dd>{r.type}</dd>
        <dt>內容</dt>
        <dd className="mono wrap-any">{r.content}</dd>
        <dt>用途</dt>
        <dd>{r.purpose}</dd>
        <dt>使用期限</dt>
        <dd>{r.expires_at || '未指定'}</dd>
        <dt>申請人</dt>
        <dd>{r.requester}</dd>
        <dt>申請時間</dt>
        <dd>{fmtDateTimeShort(r.ts)}</dd>
        {r.decided_ts && (
          <>
            <dt>處理時間</dt>
            <dd>{fmtDateTimeShort(r.decided_ts)}</dd>
          </>
        )}
      </dl>
    </Shell>
  );
}

export function LoginPage({ init }: { init: PublicInit }) {
  return (
    <div className="login-wrap" style={{ '--site-accent': init.site?.accent || '#2dd4bf' } as CSSProperties}>
      <form className="login-card" method="post" action="/login">
        <h1>管理者登入</h1>
        <p className="muted">{init.site?.title}</p>
        {init.error && (
          <div className="errbox" role="alert">
            {init.error}
          </div>
        )}
        <div className="field">
          <label htmlFor="lg-user">帳號</label>
          <input id="lg-user" name="username" className="input" autoComplete="username" required defaultValue={init.username || ''} autoFocus />
        </div>
        <div className="field">
          <label htmlFor="lg-pw">密碼</label>
          <input id="lg-pw" name="password" type="password" className="input" autoComplete="current-password" required />
        </div>
        <button type="submit" className="btn primary block">
          登入
        </button>
        <a className="back-link" href="/">
          回到服務入口
        </a>
      </form>
    </div>
  );
}

export function ClosedPage({ init }: { init: PublicInit }) {
  return (
    <Shell site={init.site} narrow>
      <h1 className="page-h">目前未開放申請</h1>
      <p className="lead">如需網域，請聯絡管理者。</p>
      <a href="/">回到服務入口</a>
    </Shell>
  );
}
