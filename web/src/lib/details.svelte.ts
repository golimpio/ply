// The full details of the items seen lately, so a page visited again paints at once. Every visit
// still refetches: the playback decision in a detail must be fresh. Cleared after every scan.
// Watch state is not kept here: a detail supplies metadata, and the list row its watch fields.
import { api, type ItemDetail } from './api';
import { app } from './state.svelte';

const LIMIT = 32;

let version = -1;
const cache = new Map<number, ItemDetail>(); // least recently used first
const inflight = new Map<number, Promise<ItemDetail>>();

export interface Detail {
  /** The copy from an earlier visit, if any: paint it while the fresh one comes. */
  cached?: ItemDetail;
  /**
   * The fresh detail. On an error the cached copy stays and the error is thrown here. An abort of
   * the caller's signal rejects with its reason, and the fetch goes on for whoever shares it.
   */
  fresh: Promise<ItemDetail>;
}

export function detail(id: number, signal?: AbortSignal): Detail {
  if (version !== app.libraryVersion) {
    version = app.libraryVersion;
    cache.clear();
    inflight.clear(); // a fetch from before the scan is not shared after it
  }
  const cached = cache.get(id);
  if (signal?.aborted) return { cached, fresh: Promise.reject(signal.reason) };
  const fresh = fetchDetail(id);
  return { cached, fresh: signal ? abortable(fresh, signal) : fresh };
}

function fetchDetail(id: number): Promise<ItemDetail> {
  const shared = inflight.get(id);
  if (shared) return shared;
  const v = app.libraryVersion;
  const p = api.item(id, 'browser').then((d) => {
    if (v === app.libraryVersion) remember(id, d); // an answer from before a scan is not kept
    return d;
  });
  inflight.set(id, p);
  const done = () => {
    if (inflight.get(id) === p) inflight.delete(id);
  };
  p.then(done, done);
  return p;
}

function remember(id: number, d: ItemDetail): void {
  cache.delete(id);
  cache.set(id, d);
  for (const old of cache.keys()) {
    if (cache.size <= LIMIT) break;
    cache.delete(old);
  }
}

function abortable<T>(p: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const abort = () => reject(signal.reason);
    signal.addEventListener('abort', abort, { once: true });
    void p.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
  });
}
