import { useSyncExternalStore } from 'react';
import { EMPTY_FILTERS, type Filters, type Sort } from './records';

/** A tiny observable value for state shared across pages and dialogs. */
export function createStore<T>(initial: T) {
  let value = initial;
  const subs = new Set<() => void>();
  return {
    get: () => value,
    set(next: T | ((prev: T) => T)) {
      value = typeof next === 'function' ? (next as (p: T) => T)(value) : next;
      subs.forEach((s) => s());
    },
    subscribe(fn: () => void) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
}

export function useStore<T>(s: ReturnType<typeof createStore<T>>) {
  return useSyncExternalStore(s.subscribe, s.get, s.get);
}

// ---- record view state (persisted) ----

export type ViewState = { filters: Filters; sort: Sort; byTarget: boolean };

const VIEW_KEY = 'cfp-admin-filters';
const DEFAULT_VIEW: ViewState = { filters: EMPTY_FILTERS, sort: { key: 'status', dir: 'desc' }, byTarget: false };

function loadView(): ViewState {
  try {
    const raw = localStorage.getItem(VIEW_KEY);
    if (raw) {
      const v = JSON.parse(raw);
      return { filters: { ...EMPTY_FILTERS, ...v.filters }, sort: v.sort ?? DEFAULT_VIEW.sort, byTarget: !!v.byTarget };
    }
  } catch {
    /* ignore */
  }
  return DEFAULT_VIEW;
}

export const viewStore = createStore<ViewState>(loadView());
viewStore.subscribe(() => {
  try {
    localStorage.setItem(VIEW_KEY, JSON.stringify(viewStore.get()));
  } catch {
    /* ignore */
  }
});

export function setFilters(patch: Partial<Filters>, replace = false) {
  viewStore.set((v) => ({ ...v, filters: replace ? { ...EMPTY_FILTERS, ...patch } : { ...v.filters, ...patch } }));
}

// ---- records waiting for a connection check after saving ----

export const pendingStore = createStore<Record<string, number>>({});

export function markPending(id: string) {
  pendingStore.set((p) => ({ ...p, [id]: Date.now() / 1000 - 1 }));
}

export function clearPending(ids: string[]) {
  if (!ids.length) return;
  pendingStore.set((p) => {
    const n = { ...p };
    ids.forEach((id) => delete n[id]);
    return n;
  });
}

// ---- dialogs ----

export type EditorFocus = 'content' | 'proxied' | 'name' | 'expires' | 'owner' | undefined;

export type ModalState =
  | { kind: 'editor'; id?: string; focus?: EditorFocus }
  | { kind: 'listing'; id: string }
  | { kind: 'delete'; id: string }
  | { kind: 'bulkMeta'; ids: string[]; focusOwner?: boolean }
  | { kind: 'bulkDelete'; ids: string[] }
  | { kind: 'batch'; mode: 'rule' | 'csv' }
  | null;

/** The record detail drawer is separate so dialogs can stack on top of it. */
export const drawerStore = createStore<string | null>(null);
export const modalStore = createStore<ModalState>(null);

export const openEditor = (id?: string, focus?: EditorFocus) => modalStore.set({ kind: 'editor', id, focus });
export const openDrawer = (id: string) => drawerStore.set(id);
export const closeModal = () => modalStore.set(null);

export const selectionStore = createStore<Set<string>>(new Set());
