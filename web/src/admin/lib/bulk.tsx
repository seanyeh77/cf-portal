import { api, errMsg } from '../../shared/api';
import { toast } from '../../shared/ui';
import { refreshRecords } from './data';

/** One record's outcome in a bulk request. */
export type BulkItem = { id: string; name: string; ok: boolean; error?: string };

/**
 * Posts a bulk request, refreshes the data and toasts the outcome, using
 * done(n) to word n successes. Resolves to the failed items (empty when all
 * succeeded), or null when the request itself failed.
 */
export async function runBulk(path: string, body: unknown, done: (n: number) => string): Promise<BulkItem[] | null> {
  let res: { results: BulkItem[]; ok: number; failed: number };
  try {
    res = await api('POST', path, body);
  } catch (e) {
    toast(errMsg(e), { error: true });
    return null;
  }
  const failed = res.results.filter((r) => !r.ok);
  if (failed.length) toast(`${done(res.ok)}，${failed.length} 筆失敗`, { error: true });
  else toast(done(res.ok));
  await refreshRecords();
  return failed;
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
