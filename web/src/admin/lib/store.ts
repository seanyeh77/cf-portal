import { useSyncExternalStore } from 'react';
import { EMPTY_FILTERS, type Filters, type Sort } from './records';

/**
 * A tiny observable value for state shared across pages and dialogs.
 * onActive runs when the first subscriber arrives and returns a function
 * that runs when the last one leaves.
 */
export function createStore<T>(initial: T, onActive?: () => () => void) {
  let value = initial;
  let stop: (() => void) | undefined;
  const subs = new Set<() => void>();
  return {
    get: () => value,
    set(next: T | ((prev: T) => T)) {
      value = typeof next === 'function' ? (next as (p: T) => T)(value) : next;
      subs.forEach((s) => s());
    },
    subscribe(fn: () => void) {
      subs.add(fn);
      if (subs.size === 1 && onActive) stop = onActive();
      return () => {
        if (subs.delete(fn) && subs.size === 0) stop?.();
      };
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

// ---- shared clock for relative times ----

const tick = () => nowStore.set(Date.now() / 1000);

// Ticks only while something shows a relative time.
const nowStore = createStore(Date.now() / 1000, () => {
  tick();
  const timer = window.setInterval(tick, 30000);
  return () => window.clearInterval(timer);
});

/** Seconds since the epoch, updated every 30 s. */
export const useNow = () => useStore(nowStore);
