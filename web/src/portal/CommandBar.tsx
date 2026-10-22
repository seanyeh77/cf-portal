import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import type { ThemeChoice } from '../shared/theme';
import { fuzzy } from './fuzzy';
import type { Service } from './Portal';

function Highlight({ text, idx }: { text: string; idx: number[] }) {
  const set = new Set(idx);
  return (
    <>
      {[...text].map((ch, i) =>
        set.has(i) ? (
          <mark key={i} className="hit">
            {ch}
          </mark>
        ) : (
          <Fragment key={i}>{ch}</Fragment>
        ),
      )}
    </>
  );
}

type Candidate = { label: string; idx: number[]; run: () => void; hint?: string };

export function CommandBar({
  services,
  categories,
  onClose,
  open,
  setCategory,
  setQuery,
  applyTheme,
  showHelp,
}: {
  services: Service[];
  categories: string[];
  onClose: () => void;
  open: (s: Service) => void;
  setCategory: (c: string) => void;
  setQuery: (q: string) => void;
  applyTheme: (t: ThemeChoice) => void;
  showHelp: () => void;
}) {
  const [text, setText] = useState('');
  const [sel, setSel] = useState(0);
  const [err, setErr] = useState('');
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => ref.current?.focus(), []);

  const cands = useMemo((): Candidate[] => {
    const raw = text.trimStart();
    const [cmd, ...restParts] = raw.split(/\s+/);
    const rest = restParts.join(' ');
    const svc = (q: string) =>
      services
        .map((s) => ({ s, m: fuzzy(s.title, q) ?? fuzzy(s.hostname, q) }))
        .filter((x) => x.m)
        .sort((a, b) => b.m!.score - a.m!.score)
        .slice(0, 8)
        .map(({ s }) => ({ label: s.title, idx: fuzzy(s.title, q)?.idx ?? [], hint: s.hostname, run: () => open(s) }) as Candidate);
    if (cmd === 'open' || cmd === 'o') return svc(rest);
    if (cmd === 'cat') {
      return ['全部', ...categories]
        .map((c) => ({ c, m: fuzzy(c, rest) }))
        .filter((x) => x.m)
        .map(({ c, m }) => ({ label: `cat ${c}`, idx: m!.idx.map((i) => i + 4), run: () => setCategory(c === '全部' ? '' : c) }));
    }
    if (cmd === 'set') {
      return (['dark', 'light', 'auto'] as const)
        .filter((t) => `bg=${t}`.startsWith(rest) || rest === '')
        .map((t) => ({ label: `set bg=${t}`, idx: [], run: () => applyTheme(t) }));
    }
    const commands: Candidate[] = [
      { label: 'open', idx: [], hint: '開啟服務', run: () => setText('open ') },
      { label: 'cat', idx: [], hint: '切換分類', run: () => setText('cat ') },
      { label: 'set bg=dark', idx: [], hint: '主題', run: () => applyTheme('dark') },
      { label: 'set bg=light', idx: [], hint: '主題', run: () => applyTheme('light') },
      { label: 'set bg=auto', idx: [], hint: '主題', run: () => applyTheme('auto') },
      { label: 'noh', idx: [], hint: '清除搜尋', run: () => setQuery('') },
      { label: 'help', idx: [], hint: '快捷鍵說明', run: showHelp },
      { label: 'q', idx: [], hint: '關閉', run: () => undefined },
    ];
    if (!raw) return commands;
    const cm = commands
      .map((c) => ({ c, m: c.label.startsWith(raw) ? { idx: [...raw].map((_, i) => i) } : null }))
      .filter((x) => x.m)
      .map(({ c, m }) => ({ ...c, idx: m!.idx }));
    return [...cm, ...svc(raw)];
  }, [text, services, categories, open, setCategory, setQuery, applyTheme, showHelp]);

  useEffect(() => setSel(0), [text]);

  const exec = () => {
    const raw = text.trim();
    if (raw === 'q' || raw === 'quit') return onClose();
    const c = cands[sel];
    if (!c) {
      setErr(raw ? `E492: Not an editor command: ${raw}` : '');
      return;
    }
    const keepOpen = c.label === 'open' || c.label === 'cat';
    c.run();
    if (!keepOpen) onClose();
  };

  return (
    <div className="cmd-overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="cmd" role="dialog" aria-label="命令列">
        <div className="cmd-input">
          <span className="colon">:</span>
          <input
            ref={ref}
            value={text}
            aria-label="命令"
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => {
              setText(e.target.value);
              setErr('');
            }}
            onKeyDown={(e) => {
              e.stopPropagation();
              const move = (d: number) => {
                e.preventDefault();
                setSel((s) => (cands.length ? (s + d + cands.length) % cands.length : 0));
              };
              if (e.key === 'Escape') {
                e.preventDefault();
                onClose();
              } else if (e.key === 'Enter') {
                e.preventDefault();
                exec();
              } else if (e.key === 'Tab') move(e.shiftKey ? -1 : 1);
              else if (e.key === 'ArrowDown' || (e.ctrlKey && e.key === 'n')) move(1);
              else if (e.key === 'ArrowUp' || (e.ctrlKey && e.key === 'p')) move(-1);
              else if (e.key === 'Backspace' && text === '') onClose();
            }}
          />
        </div>
        {err && <div className="cmd-err">{err}</div>}
        <ul className="cmd-list" role="listbox">
          {cands.map((c, i) => (
            <li
              key={c.label + i}
              role="option"
              aria-selected={i === sel}
              className={i === sel ? 'sel' : undefined}
              onMouseEnter={() => setSel(i)}
              onMouseDown={(e) => {
                e.preventDefault();
                setSel(i);
                const keepOpen = c.label === 'open' || c.label === 'cat';
                c.run();
                if (!keepOpen) onClose();
              }}
            >
              <span>
                <Highlight text={c.label} idx={c.idx} />
              </span>
              {c.hint && <span className="cmd-hint">{c.hint}</span>}
            </li>
          ))}
          {cands.length === 0 && <li className="empty">無符合項目</li>}
        </ul>
        <div className="cmd-foot">
          <kbd>Tab</kbd> <kbd>C-n</kbd> <kbd>C-p</kbd> 選擇　<kbd>Enter</kbd> 執行　<kbd>Esc</kbd> 關閉
        </div>
      </div>
    </div>
  );
}
