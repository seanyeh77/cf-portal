import { useEffect, useState } from 'react';
import { readInit } from '../shared/api';
import { ConfirmHost } from '../shared/ui';
import { useRecords } from './lib/data';
import { Overview } from './pages/Overview';
import { Records } from './pages/Records';
import { Requests } from './pages/Requests';
import { Snapshots } from './pages/Snapshots';
import { SettingsPage } from './pages/Settings';
import { AuditPage } from './pages/Audit';
import { Modals } from './dialogs/Modals';

type Init = { zone: string; title: string };

const TABS = [
  ['overview', '總覽'],
  ['records', 'DNS Records'],
  ['requests', '申請'],
  ['snapshots', '快照與還原'],
  ['settings', '網站設定'],
  ['audit', '操作紀錄'],
] as const;

export type Tab = (typeof TABS)[number][0];

function currentTab(): Tab {
  const h = window.location.hash.replace(/^#/, '');
  return (TABS.find(([k]) => k === h)?.[0] ?? 'overview') as Tab;
}

export function go(tab: Tab) {
  if (window.location.hash !== '#' + tab) window.location.hash = tab;
}

export function App() {
  const init = readInit<Init>();
  const [tab, setTab] = useState<Tab>(currentTab);
  useEffect(() => {
    const on = () => {
      setTab(currentTab());
      window.scrollTo(0, 0);
    };
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  const { data } = useRecords();
  const pending = data?.pending_requests ?? 0;
  const title = init.title || 'Service Portal';
  useEffect(() => {
    const label = TABS.find(([k]) => k === tab)?.[1];
    document.title = `${label} · ${title} 管理後台`;
  }, [tab, title]);

  return (
    <>
      <header className="topbar">
        <div className="topbar-inner">
          <div className="brand">
            <span className="brand-name">{title} 管理後台</span>
            <span className="brand-zone mono">{init.zone || data?.zone}</span>
          </div>
          <nav className="tabs" aria-label="分頁">
            {TABS.map(([k, label]) => (
              <a key={k} href={'#' + k} className={tab === k ? 'active' : undefined} aria-current={tab === k ? 'page' : undefined}>
                {label}
                {k === 'requests' && pending > 0 && <span className="lv-high tab-note">（待審 {pending}）</span>}
              </a>
            ))}
          </nav>
          <div className="topbar-right">
            <a href="/" target="_blank" rel="noreferrer">
              服務入口 ↗
            </a>
            <form method="post" action="/logout">
              <button type="submit" className="linkbtn plain">
                登出
              </button>
            </form>
          </div>
        </div>
      </header>
      <main className="page">
        {tab === 'overview' && <Overview />}
        {tab === 'records' && <Records />}
        {tab === 'requests' && <Requests />}
        {tab === 'snapshots' && <Snapshots />}
        {tab === 'settings' && <SettingsPage />}
        {tab === 'audit' && <AuditPage />}
      </main>
      <Modals />
      <ConfirmHost />
    </>
  );
}
