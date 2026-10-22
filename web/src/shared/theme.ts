export type ThemeChoice = 'auto' | 'light' | 'dark';

const KEY = 'cfp-theme';

export function getTheme(): ThemeChoice {
  try {
    const t = localStorage.getItem(KEY);
    if (t === 'light' || t === 'dark') return t;
  } catch {
    /* storage unavailable */
  }
  return 'auto';
}

export function setTheme(t: ThemeChoice) {
  try {
    if (t === 'auto') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, t);
  } catch {
    /* storage unavailable */
  }
  if (t === 'auto') document.documentElement.removeAttribute('data-theme');
  else document.documentElement.setAttribute('data-theme', t);
}

export function nextTheme(t: ThemeChoice): ThemeChoice {
  return t === 'auto' ? 'light' : t === 'light' ? 'dark' : 'auto';
}

export const themeLabel: Record<ThemeChoice, string> = { auto: '自動', light: '淺色', dark: '深色' };
