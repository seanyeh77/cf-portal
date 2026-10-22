import { useQuery, QueryClient } from '@tanstack/react-query';
import { api, errMsg } from '../../shared/api';
import { toast } from '../../shared/ui';
import type { Kind, RecordsData } from '../types';

/** The server sends kinds as an ordered list; keep it as order plus lookup. */
async function fetchRecords(path: string): Promise<RecordsData> {
  const raw = await api<Omit<RecordsData, 'kinds' | 'kind_order'> & { kinds: (Kind & { key: string })[] }>('GET', path);
  return { ...raw, kinds: Object.fromEntries(raw.kinds.map((k) => [k.key, k])), kind_order: raw.kinds.map((k) => k.key) };
}

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 5000 } },
});

/** All records with analysis. Polls faster while the server is checking. */
export function useRecords() {
  return useQuery({
    queryKey: ['records'],
    queryFn: () => fetchRecords('/api/records'),
    refetchInterval: (query) => {
      const scan = query.state.data?.scan;
      return scan && (scan.running || scan.pending.length > 0) ? 3000 : 60000;
    },
  });
}

/** Refetches all data, except queries whose first key is in keep. */
export async function refreshRecords(fresh = false, keep: string[] = []) {
  if (fresh) {
    queryClient.setQueryData(['records'], await fetchRecords('/api/records?fresh=1'));
    keep = [...keep, 'records']; // just fetched
  }
  await queryClient.invalidateQueries({ predicate: (q) => !keep.includes(String(q.queryKey[0])) });
}

/**
 * Runs a mutation, then shows okMsg and refreshes all data except the
 * queries named in keep; shows the error on failure. Resolves to whether
 * the mutation succeeded.
 */
export async function act(fn: () => Promise<unknown>, okMsg: string, keep?: string[]) {
  try {
    await fn();
  } catch (e) {
    toast(errMsg(e), { error: true });
    return false;
  }
  toast(okMsg);
  await refreshRecords(false, keep);
  return true;
}
