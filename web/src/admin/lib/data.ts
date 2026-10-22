import { useQuery, QueryClient } from '@tanstack/react-query';
import { api, errMsg } from '../../shared/api';
import { toast } from '../../shared/ui';
import type { RecordsData } from '../types';

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 5000 } },
});

/** All records with analysis. Polls faster while the server is checking. */
export function useRecords() {
  return useQuery({
    queryKey: ['records'],
    queryFn: () => api<RecordsData>('GET', '/api/records'),
    refetchInterval: (query) => {
      const scan = query.state.data?.scan;
      return scan && (scan.running || scan.pending.length > 0) ? 3000 : 60000;
    },
  });
}

/** Ids whose connection check the server is still running. */
export function pendingIds(data: RecordsData) {
  return new Set(data.scan.pending);
}

export async function refreshRecords(fresh = false) {
  if (fresh) {
    const d = await api<RecordsData>('GET', '/api/records?fresh=1');
    queryClient.setQueryData(['records'], d);
  }
  await queryClient.invalidateQueries();
}

/**
 * Runs a mutation: on success shows okMsg (if any) and refreshes all data;
 * on failure shows the error. Resolves to the result, or undefined on error.
 */
export async function act<T>(fn: () => Promise<T>, okMsg?: string | ((res: T) => string)): Promise<T | undefined> {
  try {
    const res = await fn();
    const msg = typeof okMsg === 'function' ? okMsg(res) : okMsg;
    if (msg) toast(msg);
    await refreshRecords();
    return res;
  } catch (e) {
    toast(errMsg(e), { error: true });
    return undefined;
  }
}
