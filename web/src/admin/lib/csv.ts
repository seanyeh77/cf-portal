/** Parses RFC 4180 CSV text into rows of cells. */
function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let quoted = false;
  const s = text.replace(/^﻿/, '');
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (quoted) {
      if (c === '"') {
        if (s[i + 1] === '"') {
          cell += '"';
          i++;
        } else quoted = false;
      } else cell += c;
      continue;
    }
    if (c === '"' && cell === '') quoted = true;
    else if (c === ',') {
      row.push(cell);
      cell = '';
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && s[i + 1] === '\n') i++;
      row.push(cell);
      rows.push(row);
      row = [];
      cell = '';
    } else cell += c;
  }
  if (cell !== '' || row.length) {
    row.push(cell);
    rows.push(row);
  }
  return rows.filter((r) => r.some((c) => c.trim() !== ''));
}

/** Undoes the "'" the export adds in front of formula-like cells
 * (internal/httpapi/export.go cell()). */
function unescapeCell(v: string) {
  return /^'[=+\-@\t\r']/.test(v) ? v.slice(1) : v;
}

/** Rows as objects keyed by the (lowercase) header. */
export function csvObjects(text: string): Record<string, string>[] {
  const rows = parseCSV(text);
  if (rows.length < 1) return [];
  const head = rows[0].map((h) => h.trim().toLowerCase());
  return rows.slice(1).map((r) => {
    const o: Record<string, string> = {};
    head.forEach((h, i) => (o[h] = unescapeCell((r[i] ?? '').trim())));
    return o;
  });
}

export const CSV_TEMPLATE = 'name,type,content,proxied,ttl,comment,owner,purpose,notes,expires_at,auto_delete,no_scan\nstudent001,A,10.0.3.101,false,1,,王小明,課程 VM,,2027-01-31,true,false\n';

export function truthy(v: string | undefined) {
  return /^(1|true|yes|y|是|啟用)$/i.test((v ?? '').trim());
}
