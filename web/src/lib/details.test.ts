import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ItemDetail } from './api';

vi.mock('./api', () => ({ api: { item: vi.fn() } }));

type Fn = ReturnType<typeof vi.fn>;
// Fresh module state (cache, fetches in flight) for every test.
let api: { item: Fn };
let app: { libraryVersion: number };
let lib: typeof import('./details.svelte');
beforeEach(async () => {
  vi.resetModules();
  api = (await import('./api')).api as unknown as typeof api;
  // The mock outlives resetModules. By default each request answers a new object, so a test can
  // tell one answer from another; a test queues its own answers before that.
  api.item.mockReset().mockImplementation((id: number) => Promise.resolve(item(id)));
  app = (await import('./state.svelte')).app;
  lib = await import('./details.svelte');
});

let n = 0;
const item = (id: number) => ({ id, kind: 'movie', title: `#${id} (${++n})` }) as ItemDetail;

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

/** Visit an item: what it painted at once, after the fresh one has landed. */
async function visit(id: number): Promise<ItemDetail | undefined> {
  const d = lib.detail(id);
  await d.fresh;
  return d.cached;
}

describe('detail', () => {
  it('shares a fetch in flight between callers, and refetches once it has landed', async () => {
    const a = item(1);
    const req = deferred<ItemDetail>();
    api.item.mockReturnValueOnce(req.promise);
    const first = lib.detail(1);
    const second = lib.detail(1);
    expect(api.item).toHaveBeenCalledTimes(1);
    expect(api.item).toHaveBeenCalledWith(1, 'browser');
    req.resolve(a);
    expect(await first.fresh).toBe(a);
    expect(await second.fresh).toBe(a);

    await visit(1);
    expect(api.item).toHaveBeenCalledTimes(2);
  });

  it('paints the cached copy at once and still fetches a fresh one, which replaces it', async () => {
    const first = lib.detail(1);
    expect(first.cached).toBeUndefined();
    const a = await first.fresh;

    const second = lib.detail(1);
    expect(second.cached).toBe(a);
    expect(api.item).toHaveBeenCalledTimes(2);
    const b = await second.fresh;
    expect(b).not.toBe(a);
    expect(lib.detail(1).cached).toBe(b);
  });

  it('keeps 32 details and evicts the least recently used', async () => {
    for (let id = 1; id <= 32; id++) await visit(id);
    for (let id = 1; id <= 32; id++) expect(await visit(id)).toBeDefined(); // all 32 kept, order unchanged
    expect(await visit(1)).toBeDefined(); // 1 is now the most recently used, 2 the least
    await visit(33);
    for (const id of [1, 3, 32, 33]) expect(await visit(id)).toBeDefined();
    expect(await visit(2)).toBeUndefined(); // landing again, it evicts 4, the least recent now
    expect(await visit(1)).toBeDefined();
    expect(await visit(4)).toBeUndefined();
  });

  it('is cleared when the library version changes', async () => {
    await visit(1);
    expect(await visit(1)).toBeDefined();
    app.libraryVersion++;
    expect(await visit(1)).toBeUndefined();
    expect(await visit(1)).toBeDefined();
  });

  it('does not share a fetch from before a scan, and its late answer is not kept', async () => {
    const before = deferred<ItemDetail>();
    const after = deferred<ItemDetail>();
    api.item.mockReturnValueOnce(before.promise).mockReturnValueOnce(after.promise);
    const old = lib.detail(1);
    app.libraryVersion++;
    const now = lib.detail(1);
    expect(api.item).toHaveBeenCalledTimes(2);

    const stale = item(1);
    before.resolve(stale);
    expect(await old.fresh).toBe(stale); // its own caller still gets it
    expect(lib.detail(1).cached).toBeUndefined(); // shares `now`'s fetch: no third request
    expect(api.item).toHaveBeenCalledTimes(2);

    const fresh = item(1);
    after.resolve(fresh);
    expect(await now.fresh).toBe(fresh);
    expect(lib.detail(1).cached).toBe(fresh);
  });

  it('keeps the cached copy when a refresh fails, and throws the error to the caller', async () => {
    const a = await lib.detail(1).fresh;
    api.item.mockRejectedValueOnce(new Error('HTTP 500'));
    const failed = lib.detail(1);
    expect(failed.cached).toBe(a);
    await expect(failed.fresh).rejects.toThrow('HTTP 500');
    expect(lib.detail(1).cached).toBe(a);
  });

  it('an abort rejects only its own caller: the shared fetch goes on and lands', async () => {
    const req = deferred<ItemDetail>();
    api.item.mockReturnValueOnce(req.promise);
    const ac = new AbortController();
    const leaving = lib.detail(1, ac.signal);
    const staying = lib.detail(1);
    expect(api.item).toHaveBeenCalledTimes(1);
    ac.abort();
    await expect(leaving.fresh).rejects.toMatchObject({ name: 'AbortError' });

    const a = item(1);
    req.resolve(a);
    expect(await staying.fresh).toBe(a);
    expect(lib.detail(1).cached).toBe(a);
  });

  it('a signal that does not abort gets the answer', async () => {
    const ac = new AbortController();
    const a = await lib.detail(1, ac.signal).fresh;
    expect(a.id).toBe(1);
    ac.abort(); // after the answer: nothing to reject
    expect(lib.detail(1).cached).toBe(a);
  });

  it('an already aborted signal rejects at once and starts no fetch', async () => {
    const ac = new AbortController();
    ac.abort();
    const d = lib.detail(1, ac.signal);
    await expect(d.fresh).rejects.toMatchObject({ name: 'AbortError' });
    expect(api.item).not.toHaveBeenCalled();
    await lib.detail(1).fresh;
    expect(api.item).toHaveBeenCalledTimes(1);
  });
});
