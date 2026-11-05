// The background queue, live: one EventSource (GET /api/events) opened by the app shell, applied to a
// local copy of GET /api/jobs. The Header's indicator and the Activity page read it. The queue is the
// lowest tier (after playback and browsing); this page only makes that visible.
import { api, ApiError, mediaUrl, normaliseSnapshot, normaliseTier, type Job, type JobEvent, type JobsSnapshot, type JobState, type TierState } from './api';

export const HISTORY_MAX = 200;
/** A failure this recent lights the indicator, until the Activity page has been seen. */
export const FAILURE_WINDOW_MS = 10 * 60_000;
export const POLL_MS = 3000;
/** EventSource errors in a row (without a successful open) before polling instead. */
export const SSE_MAX_FAILURES = 3;

export const EVENT_TYPES = [
  'snapshot',
  'job.queued',
  'job.updated',
  'job.held',
  'job.started',
  'job.progress',
  'job.retrying',
  'job.done',
  'job.failed',
  'job.cancelled',
  'queue.paused',
  'queue.resumed',
  'tier',
] as const;

export const isFinished = (s: JobState) => s === 'done' || s === 'failed' || s === 'cancelled';

/** Work outside the queue (reading the library folders): shown, never acted on. */
export const isSynthetic = (j: Job) => j.id < 0;

/** The queue's start order (jobs.before): priority, then age, then id. */
function startOrder(a: Job, b: Job): number {
  return a.priority - b.priority || Date.parse(a.createdAt) - Date.parse(b.createdAt) || a.id - b.id;
}

/**
 * The snapshot after one event of GET /api/events: `data` is the event's data, parsed (for a
 * snapshot, the whole queue). Jobs are upserted by id and land in running, queued or history by
 * their state. A job a worker has taken but the governor still holds stays "queued" with `held`
 * set, and belongs in running. Pure: the snapshot given is not changed.
 */
export function applyEvent(s: JobsSnapshot, type: string, data: unknown): JobsSnapshot {
  if (type === 'snapshot') return normaliseSnapshot(data as JobsSnapshot);
  const e = (data ?? {}) as Partial<JobEvent>;
  if (type === 'queue.paused' || type === 'queue.resumed') return { ...s, paused: type === 'queue.paused' };
  if (type === 'tier') return e.tier ? { ...s, tier: normaliseTier(e.tier) } : s;
  const job = e.job;
  if (!type.startsWith('job.') || !job) return s;

  const at = s.running.findIndex((j) => j.id === job.id);
  const running = s.running.filter((j) => j.id !== job.id);
  const queued = s.queued.filter((j) => j.id !== job.id);
  if (isFinished(job.state)) {
    const history = [job, ...s.history.filter((j) => j.id !== job.id)].slice(0, HISTORY_MAX);
    return { ...s, running, queued, history };
  }
  // job.retrying and job.updated are about a job no worker holds (a retry's wait, a bump, a merge).
  const taken =
    job.state === 'running' || type === 'job.held' || (at >= 0 && type !== 'job.retrying' && type !== 'job.updated' && type !== 'job.queued');
  if (taken) {
    if (at >= 0) running.splice(at, 0, job);
    else {
      // Queue jobs by id, then the work outside the queue, as the server lists them.
      const i = running.findIndex((j) => isSynthetic(j) || j.id > job.id);
      running.splice(i < 0 ? running.length : i, 0, job);
    }
  } else {
    queued.push(job);
    queued.sort(startOrder);
  }
  return { ...s, running, queued };
}

export interface Summary {
  /** Running, held ones and the work outside the queue included. */
  running: number;
  /** Of them, the ones waiting for playback or browsing. */
  held: number;
  queued: number;
  paused: boolean;
  /** The newest job that failed in the last 10 minutes, if the Activity page has not been seen since. */
  recentFailure: Job | null;
  /** Why background work waits, '' when it does not. */
  holdReason: string;
}

const ms = (iso?: string) => (iso ? Date.parse(iso) : NaN);

export function summarize(s: JobsSnapshot, now: number, seenAt: number): Summary {
  const failure = s.history.find((j) => j.state === 'failed');
  const failedAt = ms(failure?.finishedAt);
  const recent = !!failure && failedAt > now - FAILURE_WINDOW_MS && failedAt > seenAt;
  const held = s.running.filter((j) => j.held);
  return {
    running: s.running.length,
    held: held.length,
    queued: s.queued.length,
    paused: s.paused,
    recentFailure: recent ? failure! : null,
    holdReason: held[0]?.held || (s.tier.hold.held ? (s.tier.hold.reason ?? '') : ''),
  };
}

const jobs = (n: number) => `${n} background ${n === 1 ? 'job' : 'jobs'}`;

/** What the header's indicator says (its tooltip and accessible name). */
export function indicatorLabel(s: Summary): string {
  const queued = s.queued ? `, ${s.queued} queued` : '';
  if (s.recentFailure) return `A background job failed: ${s.recentFailure.title}`;
  if (s.paused) return `Background jobs paused${s.running ? `, ${s.running} finishing` : ''}${queued}`;
  if (s.running && s.held === s.running) return `${jobs(s.running)} waiting: ${s.holdReason || 'playback and browsing come first'}`;
  if (s.running) return `${jobs(s.running)} running${queued}`;
  if (s.queued) return `${jobs(s.queued)} queued`;
  return 'No background work';
}

export interface Lane {
  key: 'playback' | 'browsing' | 'queue';
  name: string;
  /** active: using the machine now; waiting: held by a tier above; idle. */
  state: 'active' | 'waiting' | 'idle';
  text: string;
}

const videos = (n: number) => `${n} ${n === 1 ? 'video' : 'videos'}`;

function playbackDetail(p: TierState['playback']): string {
  if (p.Waiting > 0) return 'a viewer is waiting for video';
  if (p.Software > 0) return 'transcoding on the CPU';
  if (p.Transcodes > 0) return 'transcoding on the hardware encoder';
  if (p.Active === 0) return 'paused';
  return 'no conversion needed';
}

/**
 * The three tiers in priority order, and one sentence that says plainly which has the machine now,
 * e.g. "Playing: 1 video (transcoding on the CPU). Background work waits."
 */
export function describeTiers(s: JobsSnapshot): { headline: string; lanes: Lane[] } {
  const { playback: p, browsing, hold } = s.tier;
  const working = s.running.filter((j) => !j.held).length;
  const playing = p.Sessions > 0;

  let headline: string;
  if (playing) {
    const rest = hold.held ? 'Background work waits.' : 'Light background work continues; anything that would compete with it waits.';
    headline = `Playing: ${videos(p.Sessions)} (${playbackDetail(p)}). ${rest}`;
  } else if (browsing) {
    headline = hold.held ? 'Someone is browsing. Background work waits a moment.' : 'Someone is browsing. Background work continues.';
  } else if (s.paused) {
    headline = 'Nothing playing. The queue is paused: nothing new starts.';
  } else {
    headline = 'Nothing playing. Background work runs freely.';
  }

  let queueText: string;
  let queueState: Lane['state'];
  if (s.paused) {
    queueText = s.running.length ? `Paused, ${s.running.length} finishing` : 'Paused';
    queueState = 'waiting';
  } else if (s.running.length && working === 0) {
    queueText = `${s.running.length} waiting their turn`;
    queueState = 'waiting';
  } else if (s.running.length) {
    queueText = `${s.running.length} running${s.queued.length ? `, ${s.queued.length} queued` : ''}`;
    queueState = 'active';
  } else if (s.queued.length) {
    queueText = `${s.queued.length} queued`;
    queueState = hold.held ? 'waiting' : 'idle';
  } else {
    queueText = 'Idle';
    queueState = 'idle';
  }

  return {
    headline,
    lanes: [
      {
        key: 'playback',
        name: 'Playback',
        state: playing ? 'active' : 'idle',
        text: playing ? `${videos(p.Sessions)}, ${playbackDetail(p)}` : 'Nothing playing',
      },
      { key: 'browsing', name: 'Browsing', state: browsing ? 'active' : 'idle', text: browsing ? 'Someone is browsing' : 'Quiet' },
      { key: 'queue', name: 'Background work', state: queueState, text: queueText },
    ],
  };
}

/** A run of queued jobs of one kind; runs shorter than `min` are left as single jobs. */
export interface QueueGroup {
  kind: string;
  jobs: Job[];
}

/** The queue in order, with long runs of one kind folded together (only when the list is long). */
export function groupQueued(list: Job[], longList = 12, min = 3): QueueGroup[] {
  const single = (j: Job): QueueGroup => ({ kind: j.kind, jobs: [j] });
  if (list.length <= longList) return list.map(single);
  const runs: QueueGroup[] = [];
  for (const j of list) {
    const last = runs.at(-1);
    if (last && last.kind === j.kind) last.jobs.push(j);
    else runs.push(single(j));
  }
  return runs.flatMap((r) => (r.jobs.length >= min ? [r] : r.jobs.map(single)));
}

/** The message shown next to a job when an action on it is refused. */
export function actionError(e: unknown, action: 'cancel' | 'bump' | 'retry'): string {
  if (e instanceof ApiError && e.status === 409) {
    if (action === 'cancel') return 'Too late to cancel: it has already finished.';
    if (action === 'bump') return 'It has already started.';
    return 'It can’t be retried now.';
  }
  if (e instanceof ApiError && e.status === 404) return 'This job is gone: finished jobs are kept for a week.';
  return e instanceof Error ? e.message : String(e);
}

// ---- the live store ---------------------------------------------------------------------------

const SEEN_KEY = 'ply.activitySeen';

function readSeen(): number {
  try {
    return Number(localStorage.getItem(SEEN_KEY)) || 0;
  } catch {
    return 0;
  }
}

export type FeedMode = 'off' | 'connecting' | 'live' | 'polling';

class Activity {
  /** Replaced whole on every change ($state.raw): progress events must stay cheap. */
  snap = $state.raw<JobsSnapshot>(normaliseSnapshot(null));
  loaded = $state(false);
  error = $state('');
  mode = $state<FeedMode>('off');
  /** Moves on every 30 s while open, for the failure window. */
  now = $state(Date.now());
  seenAt = $state(readSeen());
  summary = $derived(summarize(this.snap, this.now, this.seenAt));

  apply(type: string, data: unknown) {
    this.snap = applyEvent(this.snap, type, data);
    this.loaded = true;
    this.error = '';
  }

  /** GET /api/jobs into the store. */
  async refresh(): Promise<void> {
    try {
      this.snap = await api.jobs();
      this.loaded = true;
      this.error = '';
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  /** The Activity page is being looked at: a failure so far is no longer news. */
  markSeen() {
    this.seenAt = this.now = Date.now();
    try {
      localStorage.setItem(SEEN_KEY, String(this.seenAt));
    } catch {
      // storage unavailable: remembered until reload
    }
  }
}

export const activity = new Activity();

let source: EventSource | null = null;
let pollTimer: ReturnType<typeof setTimeout> | undefined;
let clockTimer: ReturnType<typeof setInterval> | undefined;
// Bumped by every connect and disconnect: callbacks of an older connection do nothing.
let generation = 0;

function disconnect() {
  generation++;
  source?.close();
  source = null;
  clearTimeout(pollTimer);
  clearInterval(clockTimer);
  activity.mode = 'off';
}

function poll(gen: number) {
  activity.mode = 'polling';
  const tick = async () => {
    await activity.refresh();
    if (gen === generation) pollTimer = setTimeout(tick, POLL_MS);
  };
  void tick();
}

function connect() {
  disconnect();
  const gen = generation;
  activity.now = Date.now();
  clockTimer = setInterval(() => (activity.now = Date.now()), 30_000);
  if (typeof EventSource === 'undefined') return poll(gen);

  activity.mode = 'connecting';
  const es = new EventSource(mediaUrl('/api/events'));
  source = es;
  let failures = 0;
  for (const type of EVENT_TYPES) {
    es.addEventListener(type, (ev) => {
      if (gen !== generation) return;
      try {
        activity.apply(type, JSON.parse((ev as MessageEvent<string>).data));
      } catch {
        // a malformed event: the next snapshot puts it right
      }
    });
  }
  es.onopen = () => {
    if (gen !== generation) return;
    failures = 0;
    activity.mode = 'live';
  };
  es.onerror = () => {
    if (gen !== generation) return;
    failures++;
    // CLOSED: the server refused the stream (an error status); else EventSource retries by itself.
    if (es.readyState === EventSource.CLOSED || failures >= SSE_MAX_FAILURES) {
      es.close();
      source = null;
      poll(gen);
    } else activity.mode = 'connecting';
  };
}

/**
 * Follow the queue until the returned function is called. Hidden pages stop following; they start
 * again from a fresh snapshot when they are seen.
 */
export function openActivity(): () => void {
  const onVisibility = () => (document.visibilityState === 'hidden' ? disconnect() : connect());
  document.addEventListener('visibilitychange', onVisibility);
  if (document.visibilityState !== 'hidden') connect();
  return () => {
    document.removeEventListener('visibilitychange', onVisibility);
    disconnect();
  };
}
