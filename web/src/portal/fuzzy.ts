/** Subsequence match; returns matched indices and a score (higher is better), or null. */
export function fuzzy(text: string, query: string): { score: number; idx: number[] } | null {
  const t = text.toLowerCase();
  const q = query.toLowerCase().replace(/\s+/g, '');
  if (!q) return { score: 0, idx: [] };
  const idx: number[] = [];
  let ti = 0;
  let score = 0;
  let prev = -2;
  for (const ch of q) {
    const found = t.indexOf(ch, ti);
    if (found < 0) return null;
    idx.push(found);
    score += found === prev + 1 ? 3 : 1;
    if (found === 0 || /[\s\-._/]/.test(t[found - 1] ?? '')) score += 2;
    prev = found;
    ti = found + 1;
  }
  score -= t.length * 0.01;
  return { score, idx };
}

/** Moves within a grid of cards by their on-screen position. */
export function neighbour(rects: DOMRect[], cur: number, dir: 'up' | 'down' | 'left' | 'right'): number {
  const c = rects[cur];
  if (!c) return 0;
  const cx = c.left + c.width / 2;
  const sameRow = (r: DOMRect) => Math.abs(r.top - c.top) < c.height / 2;
  let best = -1;
  let bestScore = Infinity;
  if (dir === 'left' || dir === 'right') {
    rects.forEach((r, i) => {
      if (i === cur || !sameRow(r)) return;
      const d = dir === 'right' ? r.left - c.left : c.left - r.left;
      if (d > 0 && d < bestScore) {
        bestScore = d;
        best = i;
      }
    });
    if (best < 0) return Math.max(0, Math.min(rects.length - 1, cur + (dir === 'right' ? 1 : -1)));
    return best;
  }
  // Nearest row above/below, then the card closest horizontally.
  let rowTop: number | null = null;
  rects.forEach((r) => {
    const below = r.top > c.top + c.height / 2;
    const above = r.top < c.top - c.height / 2;
    if ((dir === 'down' && below) || (dir === 'up' && above)) {
      if (rowTop === null || (dir === 'down' ? r.top < rowTop : r.top > rowTop)) rowTop = r.top;
    }
  });
  if (rowTop === null) return cur;
  rects.forEach((r, i) => {
    if (Math.abs(r.top - (rowTop as number)) > 4) return;
    const d = Math.abs(r.left + r.width / 2 - cx);
    if (d < bestScore) {
      bestScore = d;
      best = i;
    }
  });
  return best < 0 ? cur : best;
}
