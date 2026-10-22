import { useQuery } from '@tanstack/react-query';
import { memo, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';
import { api } from '../shared/api';
import { fmtHM, pct } from '../shared/format';
import { getTheme, nextTheme, setTheme, themeLabel, type ThemeChoice } from '../shared/theme';
import { copyText, Dialog } from '../shared/ui';
import { CommandBar } from './CommandBar';
import { neighbour } from './fuzzy';

export type Site = {
  title: string;
  subtitle: string;
  accent: string;
  logo: string;
  hero: string;
  announce_text: string;
  announce_level: string;
  requests_enabled: boolean;
  zone: string;
  check_interval: number;
};

export type Service = {
  id: string;
  title: string;
  description: string;
  category: string;
  url: string;
  hostname: string;
  tags: string[];
  icon: string;
  icon_url: string;
  featured: boolean;
  sort_order: number;
  created_at: number;
};

type Summary = {
  last: { ts: number; ok: boolean; ms: number; status: number } | null;
  uptime_24h: number | null;
  buckets: (number | null)[];
  bucket_start: number;
  bucket_size: number;
};

export type PortalInit = {
  portal: {
    site: Site;
    categories: { name: string; count: number }[];
    featured: string[];
    services: Service[];
    status: Record<string, Summary>;
    now: number;
  };
  admin: boolean;
};

const PALETTE = ['#0f766e', '#4f46e5', '#c2410c', '#be123c', '#0369a1', '#4d7c0f', '#7c3aed', '#475569'];
const OTHER = '其他';

/** Picks dark or white text for a background colour. */
export function inkFor(hex: string) {
  const m = hex.match(/^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i);
  if (!m) return '#ffffff';
  const [r, g, b] = [m[1], m[2], m[3]].map((x) => {
    const c = parseInt(x, 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  const L = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  return L > 0.4 ? '#0d1c1b' : '#ffffff';
}

const catOf = (s: Service) => s.category || OTHER;

function StatusBar({ sum }: { sum?: Summary }) {
  const buckets = sum?.buckets ?? Array(48).fill(null);
  const start = sum?.bucket_start ?? 0;
  const size = sum?.bucket_size ?? 1800;
  return (
    <div className="sbar" aria-hidden>
      {buckets.map((b, i) => {
        const cls = b == null ? 'nodata' : b >= 0.999 ? 'up' : b <= 0 ? 'down' : 'part';
        const from = start + i * size;
        const tip = start ? `${fmtHM(from)}–${fmtHM(from + size)} ${b == null ? '無資料' : '可用 ' + pct(b)}` : '無資料';
        return <span key={i} className={'cell ' + cls} data-tip={tip} />;
      })}
    </div>
  );
}

const Card = memo(function Card({
  s,
  color,
  sum,
  featured,
  selected,
  isNew,
  index,
}: {
  s: Service;
  color: string;
  sum?: Summary;
  featured?: boolean;
  selected: boolean;
  isNew: boolean;
  index: number;
}) {
  const last = sum?.last;
  const state = !last ? '尚未檢查' : last.ok ? '正常' : '無回應';
  const stCls = !last ? 'none' : last.ok ? 'ok' : 'down';
  const first = [...s.title][0] ?? '?';
  const style = { '--cat': color } as CSSProperties;
  return (
    <a
      className={'card' + (featured ? ' featured' : '') + (selected ? ' selected' : '')}
      href={s.url}
      target="_blank"
      rel="noreferrer"
      style={style}
      data-card={index}
      data-testid={'card-' + s.hostname}
    >
      <div className="card-top">
        <div className="card-icon">
          {s.icon_url && s.icon.startsWith('si:') ? (
            <span className="si" style={{ WebkitMaskImage: `url(${s.icon_url})`, maskImage: `url(${s.icon_url})` }} />
          ) : s.icon_url ? (
            <img src={s.icon_url} alt="" loading="lazy" />
          ) : (
            <span className="letter">{first}</span>
          )}
        </div>
        <div className="card-text">
          <div className="card-title">
            {s.title}
            {isNew ? <span className="new">NEW</span> : null}
          </div>
          {s.description && <p className="card-desc">{s.description}</p>}
          {s.tags.length > 0 && <div className="card-tags">{s.tags.join(' · ')}</div>}
        </div>
      </div>
      <StatusBar sum={sum} />
      <div className="card-status">
        <span className={'st ' + stCls}>
          <span className={'dot ' + (stCls === 'ok' ? 'ok' : stCls === 'down' ? 'down-warn' : 'none')} />
          {state}
        </span>
        {last?.ok && <span>{last.ms} ms</span>}
        {sum?.uptime_24h != null && <span data-tip="24 小時可用率">{pct(sum.uptime_24h)}</span>}
        <span className="card-url">{s.url.replace(/^https?:\/\//, '').replace(/\/$/, '')}</span>
        <span className="ext" aria-hidden>
          ↗
        </span>
      </div>
    </a>
  );
});

const HELP: [string, string][] = [
  ['j k h l、方向鍵', '在卡片間移動'],
  ['gg / G', '第一個 / 最後一個'],
  ['n / N', '下一個 / 上一個'],
  ['gt / gT', '下一個 / 上一個分類'],
  ['Ctrl-d / Ctrl-u', '往下 / 往上半頁'],
  ['o、Enter', '以新分頁開啟'],
  ['yy', '複製網址'],
  ['/', '搜尋；Enter 選第一筆，Esc 清除'],
  [':、Ctrl-k', '命令列（open、cat、set bg=dark、noh、help、q）'],
  ['?', '快捷鍵說明'],
  ['Esc', '取消選取；再按一次清除篩選'],
];

export function Portal({ init }: { init: PortalInit }) {
  const data = init.portal;
  const site = data.site;
  const interval = Math.max(15, site.check_interval || 60);
  const statusQ = useQuery({
    queryKey: ['status'],
    queryFn: () => api<{ status: Record<string, Summary>; now: number }>('GET', '/status.json'),
    initialData: { status: data.status, now: data.now },
    initialDataUpdatedAt: Date.now(),
    refetchInterval: interval * 1000,
    staleTime: interval * 1000,
  });
  const status = statusQ.data.status;
  const now = statusQ.data.now;

  const [theme, setThemeState] = useState<ThemeChoice>(getTheme);
  const applyTheme = useCallback((t: ThemeChoice) => {
    setTheme(t);
    setThemeState(t);
  }, []);
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState('');
  const [selected, setSelected] = useState(-1);
  const [cmdOpen, setCmdOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const annKey = 'cfp-announce-dismissed';
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(annKey) ?? '';
    } catch {
      return '';
    }
  });
  const searchRef = useRef<HTMLInputElement>(null);

  const categories = useMemo(() => data.categories.map((c) => c.name), [data.categories]);
  const colorOf = useCallback((c: string) => PALETTE[Math.max(0, categories.indexOf(c)) % PALETTE.length], [categories]);

  const sorted = useMemo(
    () => [...data.services].sort((a, b) => a.sort_order - b.sort_order || a.title.localeCompare(b.title, 'zh-Hant')),
    [data.services],
  );
  const filtering = query.trim() !== '' || category !== '';
  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return sorted.filter((s) => {
      if (category && catOf(s) !== category) return false;
      if (!q) return true;
      return [s.title, s.description, s.hostname, s.category, s.tags.join(' ')].join('\n').toLowerCase().includes(q);
    });
  }, [sorted, query, category]);
  const featured = useMemo(() => (filtering ? [] : visible.filter((s) => s.featured)), [filtering, visible]);
  const isNew = (s: Service) => !!s.created_at && now - s.created_at < 7 * 86400;
  const sections = useMemo(() => {
    const rest = filtering ? visible : visible.filter((s) => !s.featured);
    return categories.map((c) => ({ name: c, items: rest.filter((s) => catOf(s) === c) })).filter((x) => x.items.length);
  }, [visible, filtering, categories]);
  // Flat order matches the DOM order of cards.
  const flat = useMemo(() => [...featured, ...sections.flatMap((s) => s.items)], [featured, sections]);

  const upCount = data.services.filter((s) => status[s.id]?.last?.ok).length;
  const anyDown = data.services.some((s) => status[s.id]?.last && !status[s.id]!.last!.ok);

  useEffect(() => {
    if (selected >= flat.length) setSelected(flat.length ? flat.length - 1 : -1);
  }, [flat.length, selected]);

  const select = useCallback((i: number) => {
    setSelected(i);
    window.requestAnimationFrame(() => {
      document.querySelector(`[data-card="${i}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    });
  }, []);

  const openService = useCallback((s: Service) => {
    window.open(s.url, '_blank', 'noopener');
  }, []);

  const copy = useCallback((s: Service) => copyText(s.url, `已複製 ${s.url}`), []);

  const cycleCategory = useCallback(
    (d: number) => {
      const list = ['', ...categories];
      const i = list.indexOf(category);
      setCategory(list[(i + d + list.length) % list.length]);
      setSelected(-1);
    },
    [categories, category],
  );

  // nvim-style keys. Inputs stop propagation, so typing never reaches here.
  // The listener is registered once and reads the current state from a ref.
  const pendingKey = useRef<{ k: string; t: number } | null>(null);
  const keyState = useRef({ cmdOpen, helpOpen, selected, flat, select, copy, openService, cycleCategory });
  keyState.current = { cmdOpen, helpOpen, selected, flat, select, copy, openService, cycleCategory };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const { cmdOpen, helpOpen, selected, flat, select, copy, openService, cycleCategory } = keyState.current;
      if (cmdOpen || helpOpen || e.defaultPrevented) return;
      const t = e.target as HTMLElement;
      if (t.closest('input,textarea,select,[contenteditable=true],dialog')) return;
      if (e.altKey || e.metaKey) return;
      const k = e.key;
      if (e.ctrlKey) {
        if (k === 'k') {
          e.preventDefault();
          setCmdOpen(true);
        } else if (k === 'd' || k === 'u') {
          e.preventDefault();
          window.scrollBy({ top: (k === 'd' ? 1 : -1) * window.innerHeight * 0.5 });
          const cards = Array.from(document.querySelectorAll<HTMLElement>('[data-card]'));
          const mid = window.innerHeight / 2;
          let best = -1;
          let bd = Infinity;
          cards.forEach((c) => {
            const r = c.getBoundingClientRect();
            const d = Math.abs(r.top + r.height / 2 - mid - (k === 'd' ? 1 : -1) * window.innerHeight * 0.5);
            if (d < bd) {
              bd = d;
              best = Number(c.dataset.card);
            }
          });
          if (best >= 0) setSelected(best);
        }
        return;
      }
      const prev = pendingKey.current && Date.now() - pendingKey.current.t < 800 ? pendingKey.current.k : '';
      pendingKey.current = null;
      const cur = selected;
      const rects = () => Array.from(document.querySelectorAll<HTMLElement>('[data-card]')).map((c) => c.getBoundingClientRect());
      const move = (dir: 'up' | 'down' | 'left' | 'right') => {
        e.preventDefault();
        if (!flat.length) return;
        if (cur < 0) return select(0);
        select(neighbour(rects(), cur, dir));
      };
      if (prev === 'g') {
        if (k === 'g') return select(0);
        if (k === 't') return cycleCategory(1);
        if (k === 'T') return cycleCategory(-1);
      }
      if (prev === 'y' && k === 'y') {
        if (flat[cur]) copy(flat[cur]);
        return;
      }
      switch (k) {
        case 'j':
        case 'ArrowDown':
          return move('down');
        case 'k':
        case 'ArrowUp':
          return move('up');
        case 'h':
        case 'ArrowLeft':
          return move('left');
        case 'l':
        case 'ArrowRight':
          return move('right');
        case 'g':
        case 'y':
          pendingKey.current = { k, t: Date.now() };
          return;
        case 'G':
          return flat.length && select(flat.length - 1);
        case 'n':
          return flat.length && select(cur < 0 ? 0 : (cur + 1) % flat.length);
        case 'N':
          return flat.length && select(cur <= 0 ? flat.length - 1 : cur - 1);
        case 'o':
        case 'Enter':
          if (flat[cur]) {
            e.preventDefault();
            openService(flat[cur]);
          }
          return;
        case '/':
          e.preventDefault();
          searchRef.current?.focus();
          return;
        case ':':
          e.preventDefault();
          setCmdOpen(true);
          return;
        case '?':
          e.preventDefault();
          setHelpOpen(true);
          return;
        case 'Escape':
          if (cur >= 0) setSelected(-1);
          else {
            setQuery('');
            setCategory('');
          }
          return;
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);

  const heroStyle = site.hero ? ({ '--hero': `url(${site.hero})` } as CSSProperties) : undefined;
  const announce = site.announce_text.trim();
  const showAnnounce = announce && dismissed !== announce;
  let cardIndex = 0;

  return (
    <div className="portal" style={{ '--site-accent': site.accent || '#2dd4bf' } as CSSProperties}>
      <header className={'hero' + (site.hero ? ' has-image' : '')} style={heroStyle}>
        <div className="hero-inner">
          <div className="hero-top">
            <a className="site" href="/">
              {site.logo ? (
                <img className="logo" src={site.logo} alt="" />
              ) : (
                <span className="logo mark" style={{ background: site.accent, color: inkFor(site.accent) }}>
                  {[...site.title][0] ?? 'S'}
                </span>
              )}
              <span className="site-text">
                <span className="site-name">{site.title}</span>
                <span className="site-zone mono">{site.zone}</span>
              </span>
            </a>
            <nav className="hero-actions">
              <button type="button" className="hbtn kbd-hint" onClick={() => setHelpOpen(true)} aria-label="快捷鍵說明">
                ?
              </button>
              <button type="button" className="hbtn" onClick={() => applyTheme(nextTheme(theme))} data-tip="切換主題：自動 → 淺色 → 深色">
                主題：{themeLabel[theme]}
              </button>
              {site.requests_enabled && (
                <a className="hbtn" href="/request">
                  申請網域
                </a>
              )}
              <a className="hbtn" href={init.admin ? '/admin' : '/login'}>
                {init.admin ? '管理後台' : '管理者登入'}
              </a>
            </nav>
          </div>
          <div className="hero-title">
            <div className="eyebrow mono">SERVICE DIRECTORY</div>
            <h1>服務入口</h1>
            <p className="subtitle">{site.subtitle || `${site.zone} 服務一覽`}</p>
          </div>
          <dl className="hero-stats">
            <div>
              <dt>服務</dt>
              <dd>{data.services.length}</dd>
            </div>
            <div>
              <dt>分類</dt>
              <dd>{categories.length}</dd>
            </div>
            <div>
              <dt>目前正常</dt>
              <dd>
                <span className={'live ' + (anyDown ? 'warn' : '')} aria-hidden />
                {upCount}/{data.services.length}
              </dd>
            </div>
          </dl>
          <div className="search">
            <input
              ref={searchRef}
              type="search"
              placeholder="搜尋服務名稱、說明或網址"
              aria-label="搜尋服務"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setSelected(-1);
              }}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === 'Enter') {
                  e.preventDefault();
                  if (flat.length) {
                    searchRef.current?.blur();
                    select(0);
                  }
                } else if (e.key === 'Escape') {
                  e.preventDefault();
                  setQuery('');
                  searchRef.current?.blur();
                }
              }}
            />
            <span className="keys kbd-hint" aria-hidden>
              <kbd>/</kbd> <kbd>:</kbd>
            </span>
          </div>
          <nav className="cats" aria-label="分類">
            <button type="button" className={category === '' ? 'active' : undefined} aria-pressed={category === ''} onClick={() => setCategory('')}>
              全部 <span className="n">{data.services.length}</span>
            </button>
            {data.categories.map((c) => (
              <button key={c.name} type="button" className={category === c.name ? 'active' : undefined} aria-pressed={category === c.name} onClick={() => setCategory(c.name)}>
                {c.name} <span className="n">{c.count}</span>
              </button>
            ))}
          </nav>
        </div>
      </header>

      {showAnnounce && (
        <div className={'announce ' + (site.announce_level === 'warn' ? 'warn' : 'info')} role="status">
          <div className="announce-inner">
            <span className="announce-tag mono">{site.announce_level === 'warn' ? 'NOTICE' : 'INFO'}</span>
            <span className="announce-text">{announce}</span>
            <button
              type="button"
              className="announce-close"
              aria-label="關閉公告"
              onClick={() => {
                try {
                  localStorage.setItem(annKey, announce);
                } catch {
                  /* ignore */
                }
                setDismissed(announce);
              }}
            >
              ×
            </button>
          </div>
        </div>
      )}

      <main className="cards-main">
        {flat.length === 0 && <p className="nothing">{data.services.length ? '無符合項目' : '目前尚未上架任何服務。'}</p>}
        {featured.length > 0 && (
          <section className="section">
            <h2 className="section-title">
              ★ 精選 <span className="n">{featured.length}</span>
            </h2>
            <div className="grid featured-grid">
              {featured.map((s) => {
                const i = cardIndex++;
                return <Card key={s.id} s={s} color={colorOf(catOf(s))} sum={status[s.id]} featured selected={i === selected} isNew={isNew(s)} index={i} />;
              })}
            </div>
          </section>
        )}
        {sections.map((sec) => (
          <section key={sec.name} className="section">
            <h2 className="section-title">
              <span className="cat-mark" style={{ background: colorOf(sec.name) }} aria-hidden />
              {sec.name} <span className="n">{sec.items.length}</span>
            </h2>
            <div className="grid">
              {sec.items.map((s) => {
                const i = cardIndex++;
                return <Card key={s.id} s={s} color={colorOf(sec.name)} sum={status[s.id]} selected={i === selected} isNew={isNew(s)} index={i} />;
              })}
            </div>
          </section>
        ))}
      </main>
      <footer className="foot">
        {site.zone} · 狀態每 {interval} 秒更新
      </footer>

      {cmdOpen && (
        <CommandBar
          services={sorted}
          categories={categories}
          onClose={() => setCmdOpen(false)}
          open={openService}
          setCategory={(c) => {
            setCategory(c);
            setSelected(-1);
          }}
          setQuery={setQuery}
          applyTheme={applyTheme}
          showHelp={() => setHelpOpen(true)}
        />
      )}
      {helpOpen && (
        <Dialog title="快捷鍵" onClose={() => setHelpOpen(false)} width={520} dismissable testId="help-dialog">
          <table className="plain">
            <tbody>
              {HELP.map(([k, v]) => (
                <tr key={k}>
                  <td className="mono nowrap">{k}</td>
                  <td>{v}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="small muted">所有功能皆可用滑鼠操作，快捷鍵為選用。</p>
        </Dialog>
      )}
    </div>
  );
}
