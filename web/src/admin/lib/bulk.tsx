import { useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { toast } from '../../shared/ui';
import { refreshRecords } from './data';

/** One record's outcome in a bulk request. */
export type BulkItem = { id: string; name: string; error?: string };

/**
 * Posts a bulk request, refreshes the data and toasts the outcome, using
 * done(n) to word n successes. Resolves to the failed items, those with an
 * error (skipped ones are not failures), or null when the request failed.
 */
export async function runBulk(path: string, body: unknown, done: (n: number) => string): Promise<BulkItem[] | null> {
  let res: { results: BulkItem[]; ok: number };
  try {
    res = await api('POST', path, body);
  } catch (e) {
    toast(errMsg(e), { error: true });
    return null;
  }
  const failed = res.results.filter((r) => r.error);
  if (failed.length) toast(`${done(res.ok)}，${failed.length} 筆失敗`, { error: true });
  else toast(done(res.ok));
  await refreshRecords();
  return failed;
}

/**
 * State for a bulk dialog: run() posts through runBulk, then calls onDone
 * when everything succeeded or keeps the failures for FailedList.
 */
export function useBulk(onDone: () => void) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<BulkItem[]>([]);
  const run = async (path: string, body: unknown, done: (n: number) => string) => {
    setBusy(true);
    const bad = await runBulk(path, body, done);
    setBusy(false);
    if (bad?.length) setFailed(bad);
    else if (bad) onDone();
  };
  return { busy, failed, run };
}

/** The items a bulk request could not process, with the reason for each. */
export function FailedList({ items }: { items: BulkItem[] }) {
  if (!items.length) return null;
  return (
    <div className="warnbox" data-testid="bulk-failed">
      下列 {items.length} 筆未完成：
      <ul>
        {items.map((r) => (
          <li key={r.id}>
            <span className="mono">{r.name}</span>：{r.error}
          </li>
        ))}
      </ul>
    </div>
  );
}
