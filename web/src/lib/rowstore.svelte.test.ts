import { flushSync } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { RowStore } from './rowstore.svelte';

type R = { id: number; title: string; watched: boolean };
const row = (id: number, watched = false): R => ({ id, title: `T${id}`, watched });

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
  return { promise, resolve, reject };
}

function setup<E = undefined>(extra?: () => Promise<E>) {
  const src = { version: 0 };
  const fetch = vi.fn<() => Promise<R[]>>();
  const store = new RowStore<R, E>({ version: () => src.version, fetch, extra });
  return { src, fetch, store };
}

describe('RowStore.ensure', () => {
  it('shares one fetch per version and caches the result', async () => {
    const { fetch, store } = setup();
    const a = row(1);
    fetch.mockResolvedValue([a]);
    await Promise.all([store.ensure(), store.ensure()]);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(store.rows).toEqual([a]);
    expect(store.loaded).toBe(true);
    await store.ensure();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('fetches again after the version changes', async () => {
    const { src, fetch, store } = setup();
    fetch.mockResolvedValue([]);
    await store.ensure();
    src.version++;
    await store.ensure();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('does not reuse a fetch from before a version change, and a late old answer cannot overwrite the new one', async () => {
    const { src, fetch, store } = setup();
    const before = deferred<R[]>();
    const after = deferred<R[]>();
    fetch.mockReturnValueOnce(before.promise).mockReturnValueOnce(after.promise);
    const p1 = store.ensure();
    src.version++;
    const p2 = store.ensure();
    expect(fetch).toHaveBeenCalledTimes(2);

    after.resolve([row(2)]);
    await p2;
    expect(store.rows).toEqual([row(2)]);
    before.resolve([row(1)]);
    await p1;
    expect(store.rows).toEqual([row(2)]);
    expect(store.get(2)).toEqual(row(2));
    expect(store.get(1)).toBeUndefined();
  });

  it('reports a failure, keeps what it had, and clears the error on the next success', async () => {
    const { src, fetch, store } = setup();
    const a = row(1);
    fetch.mockResolvedValueOnce([a]).mockRejectedValueOnce(new Error('Cannot reach the ply server')).mockResolvedValueOnce([row(2)]);
    await store.ensure();
    expect(store.error).toBe('');
    src.version++;
    await store.ensure();
    expect(store.error).toBe('Cannot reach the ply server');
    expect(store.rows).toEqual([a]);
    expect(store.loaded).toBe(true);
    await store.ensure(); // a failed version is tried again
    expect(store.error).toBe('');
    expect(store.rows).toEqual([row(2)]);
  });

  it('does not report the failure of a fetch older than the list it holds', async () => {
    const { src, fetch, store } = setup();
    const before = deferred<R[]>();
    fetch.mockReturnValueOnce(before.promise).mockResolvedValueOnce([row(2)]);
    const p1 = store.ensure();
    src.version++;
    await store.ensure();
    expect(store.rows).toEqual([row(2)]);
    before.reject(new Error('HTTP 500'));
    await p1;
    expect(store.error).toBe('');
  });

  it('loads the extra with the rows, and a late old answer replaces neither', async () => {
    const shows = deferred<string[]>();
    const extra = vi.fn<() => Promise<string[]>>().mockReturnValueOnce(shows.promise).mockResolvedValueOnce(['new']);
    const { src, fetch, store } = setup(extra);
    fetch.mockResolvedValue([row(1)]);
    const p1 = store.ensure();
    src.version++;
    await store.ensure();
    expect(store.extra).toEqual(['new']);
    shows.resolve(['old']);
    await p1;
    expect(store.extra).toEqual(['new']);
  });

  it('fails the load when the extra fails', async () => {
    const { fetch, store } = setup(() => Promise.reject(new Error('HTTP 500')));
    fetch.mockResolvedValue([row(1)]);
    await store.ensure();
    expect(store.error).toBe('HTTP 500');
    expect(store.loaded).toBe(false);
  });
});

describe('RowStore.patch', () => {
  async function loaded(rows: R[]) {
    const s = setup();
    s.fetch.mockResolvedValue(rows);
    await s.store.ensure();
    return s;
  }

  it('replaces the row and the array, and leaves the other rows as they were', async () => {
    const a = row(1);
    const b = row(2);
    const { store } = await loaded([a, b]);
    const before = store.rows;
    const out = store.patch(2, { watched: true });
    expect(out).toEqual({ id: 2, title: 'T2', watched: true });
    expect(store.rows).not.toBe(before);
    expect(store.rows[1]).toBe(out);
    expect(store.get(2)).toBe(out);
    expect(b.watched).toBe(false); // the old row is not mutated
    expect(store.rows[0]).toBe(a);
    expect(store.get(1)).toBe(a);
  });

  it('patches several rows in one new array and returns the rows it found', async () => {
    const { store } = await loaded([row(1), row(2), row(3)]);
    const out = store.patchAll([
      [1, { watched: true }],
      [9, { watched: true }],
      [3, { title: 'X' }],
    ]);
    expect(out.map((r) => r.id)).toEqual([1, 3]);
    expect(store.rows).toEqual([row(1, true), row(2), { id: 3, title: 'X', watched: false }]);
  });

  it('leaves the array alone when the list lacks the row', async () => {
    const { store } = await loaded([row(1)]);
    const before = store.rows;
    expect(store.patch(1, { title: 'Y' })).toBeDefined(); // a row it has does change the array
    expect(store.rows).not.toBe(before);
    const now = store.rows;
    expect(store.patch(9, { watched: true })).toBeUndefined();
    expect(store.rows).toBe(now);
  });
});

describe('RowStore.epoch', () => {
  it('moves on when a list arrives, and not on a patch', async () => {
    const { src, fetch, store } = setup();
    fetch.mockResolvedValue([row(1)]);
    expect(store.epoch).toBe(0);
    await store.ensure();
    expect(store.epoch).toBe(1);
    store.patch(1, { watched: true });
    expect(store.rows[0].watched).toBe(true);
    expect(store.epoch).toBe(1);
    src.version++;
    await store.ensure();
    expect(store.epoch).toBe(2);
  });

  it('does not move on a failure or a discarded late answer', async () => {
    const { src, fetch, store } = setup();
    const before = deferred<R[]>();
    fetch.mockReturnValueOnce(before.promise).mockResolvedValueOnce([row(2)]).mockRejectedValueOnce(new Error('x'));
    const p1 = store.ensure();
    src.version++;
    await store.ensure();
    expect(store.epoch).toBe(1);
    before.resolve([row(1)]);
    await p1;
    expect(store.epoch).toBe(1);
    src.version++;
    await store.ensure();
    expect(store.error).toBe('x');
    expect(store.epoch).toBe(1);
  });
});

describe('RowStore write log', () => {
  it('re-applies a write made while the list was loading', async () => {
    const { src, fetch, store } = setup();
    fetch.mockResolvedValueOnce([row(1), row(2)]);
    await store.ensure();
    const next = deferred<R[]>();
    fetch.mockReturnValueOnce(next.promise);
    src.version++;
    const p = store.ensure();
    store.patch(1, { watched: true }); // marked while the list loads; the server read it before
    expect(store.rows[0].watched).toBe(true);
    const fresh = [row(1), row(2), row(3)];
    next.resolve(fresh);
    await p;
    expect(store.rows.map((r) => r.id)).toEqual([1, 2, 3]); // the new list landed
    expect(store.get(1)?.watched).toBe(true);
    expect(store.get(2)).toBe(fresh[1]); // untouched rows are the fetched ones
    expect(fresh[0].watched).toBe(false); // the fetched array and rows are not mutated
    expect(store.rows).not.toBe(fresh);
  });

  it('keeps a write made before the first list arrived, for a row the store did not have yet', async () => {
    const { fetch, store } = setup();
    const first = deferred<R[]>();
    fetch.mockReturnValueOnce(first.promise);
    const p = store.ensure();
    expect(store.patch(1, { watched: true })).toBeUndefined();
    first.resolve([row(1)]);
    await p;
    expect(store.get(1)?.watched).toBe(true);
  });

  it('does not re-apply a write made before the fetch started, though an older fetch still keeps it logged', async () => {
    const { src, fetch, store } = setup();
    const a = deferred<R[]>();
    const b = deferred<R[]>();
    fetch.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    const pa = store.ensure();
    store.patch(1, { title: 'during A' }); // the last write before B starts: B's server read has it
    src.version++;
    const pb = store.ensure();
    store.patch(2, { title: 'during B' });
    expect(store.logged).toBe(2); // both writes are in the log when B lands
    b.resolve([row(1), row(2)]);
    await pb;
    expect(store.get(2)?.title).toBe('during B');
    expect(store.get(1)?.title).toBe('T1');
    a.resolve([row(1), row(2)]);
    await pa;
    expect(store.get(1)?.title).toBe('T1');
  });

  it('keeps writes only while a fetch needs them', async () => {
    const { src, fetch, store } = setup();
    fetch.mockResolvedValueOnce([row(1)]);
    await store.ensure();
    store.patch(1, { watched: true });
    expect(store.rows[0].watched).toBe(true);
    expect(store.logged).toBe(0); // nothing in flight: nothing to keep

    const a = deferred<R[]>();
    const b = deferred<R[]>();
    fetch.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    src.version++;
    const pa = store.ensure();
    store.patch(1, { title: 'during A' });
    expect(store.logged).toBe(1);
    src.version++;
    const pb = store.ensure();
    store.patch(1, { title: 'during B' });
    expect(store.logged).toBe(2);
    b.resolve([row(1)]);
    await pb;
    // A is still out, but will be discarded: B's landing dropped everything before it started.
    expect(store.logged).toBe(0);
    store.patch(1, { title: 'after B' });
    expect(store.logged).toBe(0);
    a.resolve([row(1)]);
    await pa;
    expect(store.get(1)?.title).toBe('after B');
  });

  it('drops the writes no fetch in flight needs when a fetch fails', async () => {
    const { src, fetch, store } = setup();
    const a = deferred<R[]>();
    const b = deferred<R[]>();
    fetch.mockResolvedValueOnce([row(1)]).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    await store.ensure();
    src.version++;
    const pa = store.ensure();
    store.patch(1, { title: 'during A' });
    src.version++;
    const pb = store.ensure();
    a.reject(new Error('x'));
    await pa;
    expect(store.logged).toBe(0); // B started after the write: it does not need it
    store.patch(1, { title: 'during B' });
    expect(store.logged).toBe(1);
    b.reject(new Error('y'));
    await pb;
    expect(store.error).toBe('y');
    expect(store.logged).toBe(0);
    expect(store.get(1)?.title).toBe('during B');
  });

  it('keeps a write for an older fetch still in flight when newer ones fail', async () => {
    const { src, fetch, store } = setup();
    const a = deferred<R[]>();
    const b = deferred<R[]>();
    const c = deferred<R[]>();
    fetch.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise).mockReturnValueOnce(c.promise);
    const pa = store.ensure();
    store.patch(1, { title: 'during A' });
    src.version++;
    const pb = store.ensure();
    src.version++;
    const pc = store.ensure();
    expect(store.logged).toBe(1);
    c.reject(new Error('x'));
    await pc;
    expect(store.logged).toBe(1); // A (the oldest still out) needs it, though B (the newest) does not
    b.reject(new Error('y'));
    await pb;
    expect(store.logged).toBe(1);
    a.resolve([row(1), row(2)]);
    await pa;
    expect(store.get(1)?.title).toBe('during A');
    expect(store.get(2)).toEqual(row(2)); // A's list did land
    expect(store.logged).toBe(0);
  });

  it('settles a fetch that throws instead of rejecting, and keeps no log for it', async () => {
    const { fetch, store } = setup();
    fetch.mockImplementationOnce(() => {
      throw new Error('boom');
    });
    await store.ensure();
    expect(store.error).toBe('boom');
    store.patch(1, { title: 'after' });
    expect(store.logged).toBe(0);
    fetch.mockResolvedValueOnce([row(1)]);
    await store.ensure(); // and the same version can be tried again
    expect(store.get(1)).toEqual(row(1));
  });
});

describe('RowStore reactivity', () => {
  it('wakes a reader that found nothing before the list arrived', async () => {
    const { fetch, store } = setup();
    const seen: (R | undefined)[] = [];
    const stop = $effect.root(() => {
      $effect(() => {
        seen.push(store.get(1));
      });
    });
    flushSync();
    expect(seen).toEqual([undefined]);
    fetch.mockResolvedValue([row(1)]);
    await store.ensure();
    flushSync();
    expect(seen).toEqual([undefined, row(1)]);
    store.patch(1, { watched: true });
    flushSync();
    expect(seen.at(-1)).toEqual(row(1, true));
    stop();
  });

  it('can be patched from an effect without the effect depending on the rows', async () => {
    const { fetch, store } = setup();
    fetch.mockResolvedValue([row(1)]);
    await store.ensure();
    let runs = 0;
    const stop = $effect.root(() => {
      $effect(() => {
        runs++;
        store.patch(1, { title: `run ${runs}` });
      });
    });
    flushSync();
    expect(runs).toBe(1);
    expect(store.get(1)?.title).toBe('run 1');
    store.patch(1, { watched: true }); // another write does not re-run it either
    flushSync();
    expect(runs).toBe(1);
    stop();
  });
});
