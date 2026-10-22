import { useQuery, useQueryClient, QueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';
import { api } from '../../shared/api';
import type { RecordsData } from '../types';
import { clearPending, pendingStore, useStore } from './store';

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 5000 } },
});

/** All records with analysis. Polls faster while checks are pending. */
export function useRecords() {
  const pending = useStore(pendingStore);
  const waiting = Object.keys(pending).length > 0;
  const q = useQuery({
    queryKey: ['records'],
    queryFn: () => api<RecordsData>('GET', '/api/records'),
    refetchInterval: (query) => (waiting || query.state.data?.scan.running ? 3000 : 60000),
  });
  const data = q.data;
  useEffect(() => {
    if (!data) return;
    const done: string[] = [];
    const now = Date.now() / 1000;
    for (const [id, since] of Object.entries(pending)) {
      const r = data.records.find((x) => x.id === id);
      const ready = !r || (r.scan && r.scan.last_ts >= since) || r.meta.no_scan || !['A', 'AAAA', 'CNAME'].includes(r.type);
      if (ready || now - since > 180) done.push(id);
    }
    clearPending(done);
  }, [data, pending]);
  return q;
}

export function useRefresh() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries();
}

export async function refreshRecords(fresh = false) {
  if (fresh) {
    const d = await api<RecordsData>('GET', '/api/records?fresh=1');
    queryClient.setQueryData(['records'], d);
  }
  await queryClient.invalidateQueries();
}
