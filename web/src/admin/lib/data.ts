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

export async function refreshRecords(fresh = false) {
  if (fresh) {
    const d = await api<RecordsData>('GET', '/api/records?fresh=1');
    queryClient.setQueryData(['records'], d);
  }
  await queryClient.invalidateQueries();
}

/** Runs a mutation, then shows okMsg and refreshes all data; shows the error on failure. */
export async function act(fn: () => Promise<unknown>, okMsg: string) {
  try {
    await fn();
    toast(okMsg);
    await refreshRecords();
  } catch (e) {
    toast(errMsg(e), { error: true });
  }
}
