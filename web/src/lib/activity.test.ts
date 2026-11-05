import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, normaliseSnapshot, type Job, type JobsSnapshot } from './api';
import {
  actionError,
  applyEvent,
  describeTiers,
  FAILURE_WINDOW_MS,
  groupQueued,
  HISTORY_MAX,
  indicatorLabel,
  summarize,
} from './activity.svelte';

const T0 = Date.parse('2026-11-05T10:00:00Z');
const iso = (ms: number) => new Date(ms).toISOString();

const job = (id: number, over: Partial<Job> = {}): Job => ({
  id,
  kind: 'meta.match',
  title: `Job ${id}`,
  priority: 20,
  state: 'queued',
  user: false,
  done: 0,
  total: 0,
  attempts: 0,
  createdAt: iso(T0 + id * 1000),
  ...over,
});
const synthetic = job(-1, { kind: 'scan.folders', title: 'Read library folders', state: 'running', priority: 0 });
const snap = (over: Partial<JobsSnapshot> = {}): JobsSnapshot => ({ ...normaliseSnapshot(null), ...over });
const ids = (list: Job[]) => list.map((j) => j.id);

describe('applyEvent', () => {
  it('replaces everything with a snapshot, Go nulls as empty lists', () => {
    const s = applyEvent(snap({ queued: [job(1)] }), 'snapshot', {
      paused: true,
      running: null,
      queued: [job(2)],
      history: null,
      tier: { playback: { Sessions: 1, Active: 1, Transcodes: 0, Software: 0, Waiting: 0, Paths: null }, browsing: false, hold: { held: false } },
    });
    expect(s.paused).toBe(true);
    expect(s.running).toEqual([]);
    expect(ids(s.queued)).toEqual([2]);
    expect(s.history).toEqual([]);
    expect(s.tier.playback.Paths).toEqual([]);
    expect(s.tier.playback.Sessions).toBe(1);
  });

  it('queues a new job in start order: priority, then age, then id', () => {
    let s = snap({ queued: [job(1, { priority: 10 }), job(3, { priority: 30 })] });
    s = applyEvent(s, 'job.queued', { seq: 1, type: 'job.queued', job: job(2, { priority: 20 }) });
    expect(ids(s.queued)).toEqual([1, 2, 3]);
    s = applyEvent(s, 'job.queued', { seq: 2, type: 'job.queued', job: job(4, { priority: 10, createdAt: iso(T0) }) });
    expect(ids(s.queued)).toEqual([4, 1, 2, 3]);
  });

  it('moves a bumped job to the front (job.updated)', () => {
    const s0 = snap({ queued: [job(1, { priority: 10 }), job(2, { priority: 20 }), job(3, { priority: 30 })] });
    const s = applyEvent(s0, 'job.updated', { seq: 1, type: 'job.updated', job: job(3, { priority: 0, user: true }) });
    expect(ids(s.queued)).toEqual([3, 1, 2]);
    expect(s.queued[0].user).toBe(true);
    expect(s.running).toEqual([]);
  });

  it('puts a job a worker took but the governor holds in running, still "queued"', () => {
    const s0 = snap({ queued: [job(1), job(2)] });
    const held = job(1, { held: 'a video is playing from the same disk' });
    const s = applyEvent(s0, 'job.held', { seq: 1, type: 'job.held', job: held });
    expect(ids(s.running)).toEqual([1]);
    expect(s.running[0].state).toBe('queued');
    expect(s.running[0].held).toBe('a video is playing from the same disk');
    expect(ids(s.queued)).toEqual([2]);
    // Let go (held cleared, not yet started): it stays in running, then starts in place.
    const freed = applyEvent(s, 'job.held', { seq: 2, type: 'job.held', job: job(1) });
    expect(ids(freed.running)).toEqual([1]);
    const started = applyEvent(freed, 'job.started', { seq: 3, type: 'job.started', job: job(1, { state: 'running', startedAt: iso(T0) }) });
    expect(ids(started.running)).toEqual([1]);
    expect(started.running[0].state).toBe('running');
    expect(ids(started.queued)).toEqual([2]);
  });

  it('keeps running jobs in id order, before the work outside the queue', () => {
    const s0 = snap({ running: [job(5, { state: 'running' }), synthetic], queued: [job(3), job(7)] });
    let s = applyEvent(s0, 'job.started', { seq: 1, type: 'job.started', job: job(7, { state: 'running' }) });
    s = applyEvent(s, 'job.started', { seq: 2, type: 'job.started', job: job(3, { state: 'running' }) });
    expect(ids(s.running)).toEqual([3, 5, 7, -1]);
    expect(s.queued).toEqual([]);
  });

  it('updates progress in place', () => {
    const s0 = snap({ running: [job(1, { state: 'running' }), job(2, { state: 'running' }), synthetic] });
    const s = applyEvent(s0, 'job.progress', { seq: 1, type: 'job.progress', job: job(1, { state: 'running', done: 40, total: 100, note: 'Alien.mkv' }) });
    expect(ids(s.running)).toEqual([1, 2, -1]);
    expect(s.running[0]).toMatchObject({ done: 40, total: 100, note: 'Alien.mkv' });
  });

  it.each(['done', 'failed', 'cancelled'] as const)('moves a job that ends %s to the top of history', (state) => {
    const s0 = snap({ running: [job(1, { state: 'running' })], queued: [job(2)], history: [job(9, { state: 'done' })] });
    const s = applyEvent(s0, `job.${state}`, { seq: 1, type: `job.${state}`, job: job(1, { state, error: state === 'failed' ? 'boom' : undefined }) });
    expect(s.running).toEqual([]);
    expect(ids(s.queued)).toEqual([2]);
    expect(ids(s.history)).toEqual([1, 9]);
    expect(s.history[0].state).toBe(state);
  });

  it('takes a queued job that is cancelled out of the queue', () => {
    const s0 = snap({ queued: [job(1), job(2)] });
    expect(ids(s0.queued)).toContain(2); // positive control
    const s = applyEvent(s0, 'job.cancelled', { seq: 1, type: 'job.cancelled', job: job(2, { state: 'cancelled' }) });
    expect(ids(s.queued)).toEqual([1]);
    expect(ids(s.history)).toEqual([2]);
  });

  it('keeps at most 200 finished jobs, each once', () => {
    const history = Array.from({ length: HISTORY_MAX }, (_, i) => job(1000 - i, { state: 'done' }));
    let s = applyEvent(snap({ history }), 'job.done', { seq: 1, type: 'job.done', job: job(2000, { state: 'done' }) });
    expect(s.history).toHaveLength(HISTORY_MAX);
    expect(s.history[0].id).toBe(2000);
    expect(s.history.at(-1)!.id).toBe(1000 - HISTORY_MAX + 2);
    s = applyEvent(s, 'job.done', { seq: 2, type: 'job.done', job: job(2000, { state: 'done', note: 'again' }) });
    expect(s.history.filter((j) => j.id === 2000)).toHaveLength(1);
  });

  it('puts a job that will be retried back in the queue (job.retrying)', () => {
    const s0 = snap({ running: [job(1, { state: 'running' })], queued: [job(2, { priority: 30 })] });
    const s = applyEvent(s0, 'job.retrying', {
      seq: 1,
      type: 'job.retrying',
      job: job(1, { state: 'queued', attempts: 1, error: 'timeout', runAt: iso(T0 + 60_000) }),
    });
    expect(s.running).toEqual([]);
    expect(ids(s.queued)).toEqual([1, 2]);
    expect(s.queued[0].runAt).toBeTruthy();
  });

  it('follows pause, resume and the tiers', () => {
    let s = applyEvent(snap(), 'queue.paused', { seq: 1, type: 'queue.paused' });
    expect(s.paused).toBe(true);
    s = applyEvent(s, 'queue.resumed', { seq: 2, type: 'queue.resumed' });
    expect(s.paused).toBe(false);
    s = applyEvent(s, 'tier', { seq: 3, type: 'tier', tier: { playback: { Sessions: 2, Active: 1, Transcodes: 1, Software: 1, Waiting: 0, Paths: null }, browsing: true, hold: { held: true, reason: 'someone is browsing' } } });
    expect(s.tier.playback.Sessions).toBe(2);
    expect(s.tier.playback.Paths).toEqual([]);
    expect(s.tier.hold).toEqual({ held: true, reason: 'someone is browsing' });
  });

  it('ignores events it does not know, and never changes the snapshot it was given', () => {
    const s0 = snap({ running: [job(1, { state: 'running' })], queued: [job(2)] });
    const copy = structuredClone(s0);
    expect(applyEvent(s0, 'job.mystery', { seq: 1, type: 'job.mystery' })).toBe(s0);
    expect(applyEvent(s0, 'whatever', {})).toBe(s0);
    applyEvent(s0, 'job.done', { seq: 2, type: 'job.done', job: job(1, { state: 'done' }) });
    applyEvent(s0, 'job.started', { seq: 3, type: 'job.started', job: job(2, { state: 'running' }) });
    expect(s0).toEqual(copy);
  });
});

describe('summarize', () => {
  const failedAt = (ms: number) => job(7, { state: 'failed', error: 'TMDB timed out', finishedAt: iso(ms) });

  it('counts running (held and outside the queue included), queued and held', () => {
    const s = summarize(
      snap({ running: [job(1, { state: 'running' }), job(2, { held: 'someone is browsing' }), synthetic], queued: [job(3), job(4)] }),
      T0,
      0,
    );
    expect(s).toMatchObject({ running: 3, held: 1, queued: 2, paused: false, recentFailure: null, holdReason: 'someone is browsing' });
  });

  it('reports a failure of the last 10 minutes until the page is seen', () => {
    const s = snap({ history: [failedAt(T0 - 60_000)] });
    expect(summarize(s, T0, 0).recentFailure?.id).toBe(7);
    expect(summarize(s, T0, T0 - 30_000).recentFailure).toBeNull(); // seen since
    expect(summarize(s, T0 - 60_000 + FAILURE_WINDOW_MS + 1, 0).recentFailure).toBeNull(); // too old
    // Only the newest failure counts, and only a failure.
    expect(summarize(snap({ history: [job(8, { state: 'done', finishedAt: iso(T0) }), failedAt(T0 - 1000)] }), T0, 0).recentFailure?.id).toBe(7);
    expect(summarize(snap({ history: [job(8, { state: 'cancelled', finishedAt: iso(T0) })] }), T0, 0).recentFailure).toBeNull();
  });

  it('takes the hold reason from the tiers when no running job is held', () => {
    const s = snap({ tier: { ...normaliseSnapshot(null).tier, hold: { held: true, reason: 'a viewer is waiting for video' } } });
    expect(summarize(s, T0, 0).holdReason).toBe('a viewer is waiting for video');
    expect(summarize(snap(), T0, 0).holdReason).toBe('');
  });
});

describe('indicatorLabel', () => {
  const base = { running: 0, held: 0, queued: 0, paused: false, recentFailure: null, holdReason: '' };
  it.each([
    [{}, 'No background work'],
    [{ running: 1 }, '1 background job running'],
    [{ running: 2, queued: 5 }, '2 background jobs running, 5 queued'],
    [{ queued: 3 }, '3 background jobs queued'],
    [{ running: 1, held: 1, holdReason: 'someone is browsing' }, '1 background job waiting: someone is browsing'],
    [{ paused: true, queued: 4 }, 'Background jobs paused, 4 queued'],
    [{ paused: true, running: 1 }, 'Background jobs paused, 1 finishing'],
    [{ running: 1, recentFailure: job(3, { title: 'Scan the library' }) }, 'A background job failed: Scan the library'],
  ])('%o', (over, want) => {
    expect(indicatorLabel({ ...base, ...over })).toBe(want);
  });
});

describe('describeTiers', () => {
  const tier = (playback: Partial<JobsSnapshot['tier']['playback']> = {}, browsing = false, held = false) => ({
    playback: { Sessions: 0, Active: 0, Transcodes: 0, Software: 0, Waiting: 0, Paths: [], ...playback },
    browsing,
    hold: { held },
  });

  it('says when nothing plays and the queue is free', () => {
    const d = describeTiers(snap());
    expect(d.headline).toBe('Nothing playing. Background work runs freely.');
    expect(d.lanes.map((l) => [l.key, l.state])).toEqual([
      ['playback', 'idle'],
      ['browsing', 'idle'],
      ['queue', 'idle'],
    ]);
  });

  it('says playback has the machine, and how', () => {
    const d = describeTiers(snap({ tier: tier({ Sessions: 1, Active: 1, Transcodes: 1, Software: 1 }, false, true), running: [job(1, { held: 'x' })] }));
    expect(d.headline).toBe('Playing: 1 video (transcoding on the CPU). Background work waits.');
    expect(d.lanes[0]).toMatchObject({ state: 'active', text: '1 video, transcoding on the CPU' });
    expect(d.lanes[2]).toMatchObject({ state: 'waiting', text: '1 waiting their turn' });
    expect(describeTiers(snap({ tier: tier({ Sessions: 2, Active: 2, Waiting: 1 }, false, true) })).headline).toMatch(/^Playing: 2 videos \(a viewer is waiting for video\)/);
    expect(describeTiers(snap({ tier: tier({ Sessions: 1, Active: 1 }) })).headline).toMatch(/^Playing: 1 video \(no conversion needed\)\. Light background work continues/);
  });

  it('says when someone browses, and when the queue is paused', () => {
    expect(describeTiers(snap({ tier: tier({}, true, true) })).headline).toBe('Someone is browsing. Background work waits a moment.');
    const p = describeTiers(snap({ paused: true, queued: [job(1)] }));
    expect(p.headline).toBe('Nothing playing. The queue is paused: nothing new starts.');
    expect(p.lanes[2]).toMatchObject({ state: 'waiting', text: 'Paused' });
  });
});

describe('groupQueued', () => {
  const run = (kind: string, from: number, n: number) => Array.from({ length: n }, (_, i) => job(from + i, { kind }));

  it('leaves a short queue as it is', () => {
    const list = run('meta.match', 1, 5);
    expect(groupQueued(list).map((g) => g.jobs.length)).toEqual([1, 1, 1, 1, 1]);
  });

  it('folds long runs of one kind in a long queue, keeping the order', () => {
    const list = [...run('scan.dirs', 1, 2), ...run('meta.match', 10, 10), ...run('cache.prune', 30, 1), ...run('meta.match', 40, 3)];
    const g = groupQueued(list);
    expect(g.map((x) => [x.kind, x.jobs.length])).toEqual([
      ['scan.dirs', 1],
      ['scan.dirs', 1],
      ['meta.match', 10],
      ['cache.prune', 1],
      ['meta.match', 3],
    ]);
    expect(g.flatMap((x) => ids(x.jobs))).toEqual(ids(list));
  });
});

describe('actionError', () => {
  it('says why an action was refused', () => {
    expect(actionError(new ApiError(409, 'x'), 'cancel')).toMatch(/already finished/);
    expect(actionError(new ApiError(409, 'x'), 'bump')).toMatch(/already started/);
    expect(actionError(new ApiError(404, 'x'), 'retry')).toMatch(/gone/);
    expect(actionError(new ApiError(500, 'disk full'), 'retry')).toBe('disk full');
  });
});

// ---- the live feed ---------------------------------------------------------------------------

class FakeEventSource {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;
  static all: FakeEventSource[] = [];
  readyState = 0;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeEventSource.all.push(this);
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    (this.listeners[type] ??= []).push(fn);
  }
  close() {
    this.readyState = 2;
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners[type] ?? []) fn(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  fail(closed = false) {
    this.readyState = closed ? 2 : 0;
    this.onerror?.();
  }
}

describe('openActivity', () => {
  let jobs: ReturnType<typeof vi.fn>;
  let mod: typeof import('./activity.svelte');
  let visibility: DocumentVisibilityState;

  beforeEach(async () => {
    vi.useFakeTimers();
    vi.resetModules();
    FakeEventSource.all = [];
    jobs = vi.fn(() => Promise.resolve(snap({ queued: [job(1)] })));
    vi.doMock('./api', async (orig) => {
      const real = await orig<typeof import('./api')>();
      return { ...real, api: { ...real.api, jobs } };
    });
    visibility = 'visible';
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility);
    vi.stubGlobal('EventSource', FakeEventSource);
    mod = await import('./activity.svelte');
  });
  afterEach(() => {
    vi.doUnmock('./api');
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  const setVisible = (v: DocumentVisibilityState) => {
    visibility = v;
    document.dispatchEvent(new Event('visibilitychange'));
  };

  it('follows the events of one EventSource on /api/events', () => {
    const close = mod.openActivity();
    expect(FakeEventSource.all).toHaveLength(1);
    const es = FakeEventSource.all[0];
    expect(new URL(es.url).pathname).toBe('/api/events');
    es.open();
    expect(mod.activity.mode).toBe('live');
    es.emit('snapshot', snap({ running: [job(1, { state: 'running' })] }));
    es.emit('job.queued', { seq: 2, type: 'job.queued', job: job(2) });
    expect(mod.activity.loaded).toBe(true);
    expect(mod.activity.summary.running).toBe(1);
    expect(mod.activity.summary.queued).toBe(1);
    expect(jobs).not.toHaveBeenCalled();
    close();
    expect(es.readyState).toBe(FakeEventSource.CLOSED);
  });

  it('polls GET /api/jobs every 3 s when there is no EventSource', async () => {
    vi.stubGlobal('EventSource', undefined);
    const close = mod.openActivity();
    await vi.advanceTimersByTimeAsync(0);
    expect(mod.activity.mode).toBe('polling');
    expect(jobs).toHaveBeenCalledTimes(1);
    expect(mod.activity.summary.queued).toBe(1);
    await vi.advanceTimersByTimeAsync(mod.POLL_MS);
    expect(jobs).toHaveBeenCalledTimes(2);
    close();
    await vi.advanceTimersByTimeAsync(mod.POLL_MS * 3);
    expect(jobs).toHaveBeenCalledTimes(2);
  });

  it('falls back to polling after repeated errors, not after one', async () => {
    const close = mod.openActivity();
    const es = FakeEventSource.all[0];
    es.fail();
    es.fail();
    await vi.advanceTimersByTimeAsync(0);
    expect(mod.activity.mode).toBe('connecting'); // EventSource retries by itself
    expect(jobs).not.toHaveBeenCalled();
    // An open in between starts the count again.
    es.open();
    es.fail();
    es.fail();
    expect(jobs).not.toHaveBeenCalled();
    es.fail();
    await vi.advanceTimersByTimeAsync(0);
    expect(es.readyState).toBe(FakeEventSource.CLOSED);
    expect(mod.activity.mode).toBe('polling');
    expect(jobs).toHaveBeenCalledTimes(1);
    close();
  });

  it('polls at once when the server refuses the stream', async () => {
    const close = mod.openActivity();
    FakeEventSource.all[0].fail(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(mod.activity.mode).toBe('polling');
    expect(jobs).toHaveBeenCalledTimes(1);
    close();
  });

  it('closes while the page is hidden and starts again from a fresh stream', () => {
    const close = mod.openActivity();
    const first = FakeEventSource.all[0];
    first.open();
    setVisible('hidden');
    expect(first.readyState).toBe(FakeEventSource.CLOSED);
    expect(mod.activity.mode).toBe('off');
    expect(FakeEventSource.all).toHaveLength(1);
    setVisible('visible');
    expect(FakeEventSource.all).toHaveLength(2);
    // Events of the old stream are not applied any more.
    first.emit('job.queued', { seq: 9, type: 'job.queued', job: job(5) });
    expect(mod.activity.summary.queued).toBe(0);
    FakeEventSource.all[1].emit('snapshot', snap({ queued: [job(1), job(2)] }));
    expect(mod.activity.summary.queued).toBe(2);
    close();
    setVisible('hidden');
    setVisible('visible');
    expect(FakeEventSource.all).toHaveLength(2); // closed for good
  });

  it('markSeen clears the failure light and remembers it', () => {
    mod.activity.apply('snapshot', snap({ history: [job(7, { state: 'failed', finishedAt: iso(Date.now() - 1000) })] }));
    expect(mod.activity.summary.recentFailure?.id).toBe(7);
    mod.activity.markSeen();
    expect(mod.activity.summary.recentFailure).toBeNull();
    expect(Number(localStorage.getItem('ply.activitySeen'))).toBeGreaterThan(0);
  });
});
