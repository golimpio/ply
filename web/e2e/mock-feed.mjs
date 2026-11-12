// The live feed as ply sends it (GET /api/events; docs/M11-CLIENTS.md, "Live feed"), for the e2e mocks.

/** The tiers (tier.State): nothing playing, nobody browsing, nothing held, unless said. */
export const tierState = (playback = {}, browsing = false, hold = { held: false }) => ({
  playback: { Sessions: 0, Active: 0, Transcodes: 0, Software: 0, Waiting: 0, Paths: null, ...playback },
  browsing,
  hold,
});

/** The background queue (jobs.Snapshot, GET /api/jobs): idle, unless over says otherwise. */
export const jobsSnapshot = (over = {}) => ({ paused: false, running: [], queued: [], history: [], tier: tierState(), ...over });

/**
 * GET /api/events, for route.fulfill: the retry, the snapshot as id 1 (feedSnapshot in
 * internal/api/activity.go; its tier, which the server reads from the governor, is the queue's here), then each
 * [type, data] of events with the ids that follow. The stream then ends, and EventSource reconnects after retry
 * ms (the server's is 3 s).
 */
export function eventStream({ jobs = jobsSnapshot(), sessions = [], retry = 3000, events = [] } = {}) {
  let body = `retry: ${retry}\n\nid: 1\nevent: snapshot\ndata: ${JSON.stringify({ api: 1, jobs, sessions, tier: jobs.tier })}\n\n`;
  for (const [i, [type, data]] of events.entries()) body += `id: ${2 + i}\nevent: ${type}\ndata: ${JSON.stringify(data ?? {})}\n\n`;
  return { status: 200, headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' }, body };
}
