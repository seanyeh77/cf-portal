import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { onLayerChange, popLayer, pushLayer, topLayer } from './layers';

/** A circular "?" that shows its help text on hover, focus or click. */
export function Tip({ text, label = '說明' }: { text: string; label?: string }) {
  return (
    <button type="button" className="tip-icon" data-tip={text} aria-label={label} aria-expanded="false">
      ?
    </button>
  );
}

const DialogDepth = createContext(0);

type DialogProps = {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
  className?: string;
  /** Close when the backdrop is clicked. */
  dismissable?: boolean;
  labelledBy?: string;
  testId?: string;
};

/** A native modal <dialog> opened with showModal(). Mounting opens it. */
export function Dialog({ title, onClose, children, footer, width, className, dismissable, testId }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const depth = useContext(DialogDepth);
  useLayoutEffect(() => {
    const d = ref.current!;
    if (!d.open) d.showModal();
    // showModal() focuses the first focusable element; honour an explicit
    // initial focus such as the 取消 button of a confirmation.
    d.querySelector<HTMLElement>('[data-autofocus]')?.focus();
    pushLayer(d);
    return () => {
      popLayer(d);
      if (d.open) d.close();
    };
  }, []);
  return (
    <DialogDepth.Provider value={depth + 1}>
      <dialog
        ref={ref}
        className={'dlg ' + (className ?? '')}
        style={width ? { width } : undefined}
        data-testid={testId}
        onCancel={(e) => {
          e.preventDefault();
          closeRef.current();
        }}
        onMouseDown={(e) => {
          if (dismissable && e.target === ref.current) {
            const r = ref.current.getBoundingClientRect();
            const inside = e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
            if (!inside) closeRef.current();
          }
        }}
      >
        <div className="dlg-head">
          <h2>{title}</h2>
          <button type="button" className="dlg-close" aria-label="關閉" onClick={() => closeRef.current()}>
            ×
          </button>
        </div>
        <div className="dlg-body">{children}</div>
        {footer && <div className="dlg-foot">{footer}</div>}
      </dialog>
    </DialogDepth.Provider>
  );
}

export type MenuItem =
  | { label: string; onClick: () => void; danger?: boolean; disabled?: boolean; tip?: string; testId?: string }
  | 'sep';

/** Re-renders when the modal layer stack changes. */
function useLayerHost() {
  const [host, setHost] = useState<HTMLElement>(() => topLayer());
  useEffect(() => onLayerChange(() => setHost(topLayer())), []);
  return host;
}

/** A floating menu anchored to a button. */
export function MenuButton({
  label,
  items,
  className = 'btn',
  ariaLabel,
  title,
  testId,
  align = 'right',
}: {
  label: ReactNode;
  items: MenuItem[];
  className?: string;
  ariaLabel?: string;
  title?: string;
  testId?: string;
  align?: 'left' | 'right';
}) {
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  return (
    <>
      <button
        ref={btn}
        type="button"
        className={className}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={ariaLabel}
        data-tip={title}
        data-testid={testId}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
      >
        {label}
      </button>
      {open && <FloatingMenu anchor={btn.current!} items={items} align={align} onClose={() => setOpen(false)} />}
    </>
  );
}

export function FloatingMenu({
  anchor,
  items,
  onClose,
  align = 'right',
}: {
  anchor: HTMLElement;
  items: MenuItem[];
  onClose: () => void;
  align?: 'left' | 'right';
}) {
  const ref = useRef<HTMLDivElement>(null);
  const host = useLayerHost();
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  useLayoutEffect(() => {
    const r = anchor.getBoundingClientRect();
    const m = ref.current!;
    const w = m.offsetWidth;
    const h = m.offsetHeight;
    let left = align === 'right' ? r.right - w : r.left;
    left = Math.max(8, Math.min(left, window.innerWidth - w - 8));
    let top = r.bottom + 4;
    if (top + h > window.innerHeight - 8) top = Math.max(8, r.top - h - 4);
    setPos({ left, top });
    const first = m.querySelector<HTMLButtonElement>('button:not(:disabled)');
    first?.focus({ preventScroll: true });
  }, [anchor, align]);
  useEffect(() => {
    const down = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node) && !anchor.contains(e.target as Node)) onClose();
    };
    // Scrolling that started before the menu opened (e.g. scrolling the
    // button into view) must not close it.
    const openedAt = performance.now();
    const scroll = (e: Event) => {
      if (performance.now() - openedAt < 200) return;
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        onClose();
        anchor.focus();
      }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const btns = Array.from(ref.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []);
        const i = btns.indexOf(document.activeElement as HTMLButtonElement);
        const next = btns[(i + (e.key === 'ArrowDown' ? 1 : -1) + btns.length) % btns.length];
        next?.focus();
      }
    };
    document.addEventListener('mousedown', down, true);
    window.addEventListener('scroll', scroll, true);
    window.addEventListener('resize', onClose);
    document.addEventListener('keydown', key, true);
    return () => {
      document.removeEventListener('mousedown', down, true);
      window.removeEventListener('scroll', scroll, true);
      window.removeEventListener('resize', onClose);
      document.removeEventListener('keydown', key, true);
    };
  }, [anchor, onClose]);
  return createPortal(
    <div
      ref={ref}
      className="menu"
      role="menu"
      style={pos ? { left: pos.left, top: pos.top } : { left: -9999, top: 0 }}
      onClick={(e) => e.stopPropagation()}
    >
      {items.map((it, i) =>
        it === 'sep' ? (
          <hr key={i} />
        ) : (
          <button
            key={i}
            type="button"
            role="menuitem"
            className={it.danger ? 'danger' : undefined}
            disabled={it.disabled}
            data-tip={it.tip}
            data-testid={it.testId}
            onClick={() => {
              onClose();
              it.onClick();
            }}
          >
            {it.label}
          </button>
        ),
      )}
    </div>,
    host,
  );
}

// ---- toasts ----

/** Shows a message at the bottom centre; errors stay longer and are red. */
export function toast(msg: string, opts: { error?: boolean } = {}) {
  // Placed after the current render so a dialog that is closing in the same
  // step does not take the toast with it.
  window.setTimeout(() => showToast(msg, opts), 40);
}

function showToast(msg: string, opts: { error?: boolean }) {
  const layer = topLayer();
  let box = layer.querySelector<HTMLDivElement>(':scope > .toasts');
  if (!box) {
    box = document.createElement('div');
    box.className = 'toasts';
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');
    layer.appendChild(box);
  }
  const t = document.createElement('div');
  t.className = 'toast' + (opts.error ? ' error' : '');
  t.textContent = msg;
  box.appendChild(t);
  window.setTimeout(() => t.remove(), opts.error ? 6000 : 2600);
}

// ---- confirm ----

export type ConfirmOptions = {
  title: string;
  body: ReactNode;
  confirmText?: string;
  danger?: boolean;
  width?: number;
};

type Pending = ConfirmOptions & { resolve: (ok: boolean) => void };

let pushConfirm: ((p: Pending) => void) | null = null;

/** Opens a confirmation dialog; resolves true only when confirmed. */
export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => {
    if (!pushConfirm) {
      resolve(window.confirm(opts.title));
      return;
    }
    pushConfirm({ ...opts, resolve });
  });
}

/** Renders confirm dialogs; mount once per app. Focus starts on 取消. */
export function ConfirmHost() {
  const [queue, setQueue] = useState<Pending[]>([]);
  useEffect(() => {
    pushConfirm = (p) => setQueue((q) => [...q, p]);
    return () => {
      pushConfirm = null;
    };
  }, []);
  const cur = queue[0];
  const done = useCallback(
    (ok: boolean) => {
      cur?.resolve(ok);
      setQueue((q) => q.slice(1));
    },
    [cur],
  );
  if (!cur) return null;
  return (
    <Dialog
      key={queue.length}
      title={cur.title}
      onClose={() => done(false)}
      width={cur.width ?? 480}
      testId="confirm-dialog"
      footer={
        <>
          <button type="button" className="btn" data-autofocus onClick={() => done(false)}>
            取消
          </button>
          <button type="button" className={'btn ' + (cur.danger ? 'danger' : 'primary')} onClick={() => done(true)}>
            {cur.confirmText ?? '確認'}
          </button>
        </>
      }
    >
      {cur.body}
    </Dialog>
  );
}
