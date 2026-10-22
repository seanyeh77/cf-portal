import { topLayer } from './layers';

// One tooltip for the whole document. Any element with data-tip shows it on
// hover and keyboard focus; elements with the tip-icon class also toggle it
// on click so touch users can read it. The text is set with textContent, so
// tips never render HTML.

let el: HTMLDivElement | null = null;
let current: HTMLElement | null = null;
let pinned = false;

function tipOf(t: EventTarget | null): HTMLElement | null {
  if (!(t instanceof Element)) return null;
  const host = t.closest<HTMLElement>('[data-tip]');
  return host && host.dataset.tip ? host : null;
}

function place(target: HTMLElement) {
  if (!el) return;
  const r = target.getBoundingClientRect();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  el.style.left = '0px';
  el.style.top = '0px';
  el.style.maxWidth = Math.min(340, vw - 16) + 'px';
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  let left = r.left + r.width / 2 - w / 2;
  left = Math.max(8, Math.min(left, vw - w - 8));
  let top = r.top - h - 8;
  if (top < 8) top = r.bottom + 8;
  if (top + h > vh - 8) top = Math.max(8, vh - h - 8);
  el.style.left = Math.round(left) + 'px';
  el.style.top = Math.round(top) + 'px';
}

export function showTip(target: HTMLElement) {
  const text = target.dataset.tip;
  if (!text) return;
  if (!el) {
    el = document.createElement('div');
    el.className = 'tooltip';
    el.setAttribute('role', 'tooltip');
    el.id = 'cfp-tooltip';
  }
  const layer = topLayer();
  if (el.parentElement !== layer) layer.appendChild(el);
  el.textContent = text;
  el.hidden = false;
  if (current && current !== target) current.removeAttribute('aria-describedby');
  current = target;
  target.setAttribute('aria-describedby', 'cfp-tooltip');
  if (target.classList.contains('tip-icon')) target.setAttribute('aria-expanded', 'true');
  place(target);
}

export function hideTip() {
  if (el) el.hidden = true;
  if (current) {
    current.removeAttribute('aria-describedby');
    if (current.classList.contains('tip-icon')) current.setAttribute('aria-expanded', 'false');
  }
  current = null;
  pinned = false;
}

let installed = false;

export function installTooltips() {
  if (installed) return;
  installed = true;
  document.addEventListener('mouseover', (e) => {
    if (pinned) return;
    const t = tipOf(e.target);
    if (t && t !== current) showTip(t);
  });
  document.addEventListener('mouseout', (e) => {
    if (pinned || !current) return;
    const to = e.relatedTarget as Node | null;
    if (to && current.contains(to)) return;
    if (tipOf(e.target) === current) hideTip();
  });
  document.addEventListener('focusin', (e) => {
    const t = tipOf(e.target);
    if (t) showTip(t);
    else if (!pinned) hideTip();
  });
  document.addEventListener('focusout', (e) => {
    if (!pinned && tipOf(e.target) === current) hideTip();
  });
  document.addEventListener(
    'click',
    (e) => {
      const t = tipOf(e.target);
      if (t && t.classList.contains('tip-icon')) {
        e.preventDefault();
        e.stopPropagation();
        if (pinned && current === t) {
          hideTip();
        } else {
          showTip(t);
          pinned = true;
        }
        return;
      }
      if (pinned) hideTip();
    },
    true,
  );
  window.addEventListener('scroll', () => current && hideTip(), true);
  window.addEventListener('resize', () => current && hideTip());
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && current) hideTip();
  });
  // A closing dialog removes its tooltip host.
  document.addEventListener('close', () => hideTip(), true);
}
