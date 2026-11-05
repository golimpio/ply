import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { HEARTBEAT_MS, Heartbeat } from './heartbeat';

/** A player whose position and paused state the test sets; `answer` decides what each beat gets back. */
function setup(answer: (n: number) => Promise<unknown> = () => Promise.resolve()) {
  const player = { pos: 12.5, paused: false };
  const send = vi.fn((_sid: string, _pos: number, _paused: boolean) => answer(send.mock.calls.length));
  const onGone = vi.fn();
  const onStopped = vi.fn();
  const hb = new Heartbeat({ send, position: () => player.pos, paused: () => player.paused, onGone, onStopped });
  return { player, send, onGone, onStopped, hb };
}

/** Let the promise callbacks of the beats run. */
const settle = () => vi.advanceTimersByTimeAsync(0);

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('Heartbeat', () => {
  it('beats every 10 s for the session it was given, with the position', () => {
    const { send, hb } = setup();
    hb.start('s1');
    expect(send).not.toHaveBeenCalled();
    vi.advanceTimersByTime(HEARTBEAT_MS - 1);
    expect(send).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenLastCalledWith('s1', 12.5, false);
    vi.advanceTimersByTime(HEARTBEAT_MS * 3);
    expect(send).toHaveBeenCalledTimes(4);
    expect(HEARTBEAT_MS).toBe(10_000);
  });

  it('beats at once when asked (on pause and on play), with the paused state', () => {
    const { player, send, hb } = setup();
    hb.start('s1');
    player.paused = true;
    hb.beat();
    expect(send).toHaveBeenLastCalledWith('s1', 12.5, true);
    player.paused = false;
    player.pos = 13;
    hb.beat();
    expect(send).toHaveBeenLastCalledWith('s1', 13, false);
    expect(send).toHaveBeenCalledTimes(2);
  });

  it('keeps beating while the player is paused', () => {
    const { player, send, hb } = setup();
    hb.start('s1');
    player.paused = true;
    vi.advanceTimersByTime(HEARTBEAT_MS * 5);
    expect(send).toHaveBeenCalledTimes(5);
    expect(send.mock.calls.every(([, , paused]) => paused === true)).toBe(true);
  });

  it('stops on close, and a beat asked for after that sends nothing', () => {
    const { send, hb } = setup();
    hb.start('s1');
    vi.advanceTimersByTime(HEARTBEAT_MS);
    expect(send).toHaveBeenCalledTimes(1); // positive control
    hb.stop();
    hb.beat();
    vi.advanceTimersByTime(HEARTBEAT_MS * 5);
    expect(send).toHaveBeenCalledTimes(1);
    expect(hb.session).toBeUndefined();
  });

  it('sends nothing before it has a session', () => {
    const { send, hb } = setup();
    hb.beat();
    vi.advanceTimersByTime(HEARTBEAT_MS * 3);
    expect(send).not.toHaveBeenCalled();
  });

  it('moves to the next session: beats only for the latest', () => {
    const { send, hb } = setup();
    hb.start('s1');
    hb.start('s2');
    vi.advanceTimersByTime(HEARTBEAT_MS * 2);
    expect(send.mock.calls.map(([sid]) => sid)).toEqual(['s2', 's2']);
  });

  it('a 404 calls onGone once, with the last position, and stops beating', async () => {
    const { player, send, onGone, hb } = setup(() => Promise.reject(new ApiError(404, 'session not found')));
    hb.start('s1');
    player.paused = true;
    player.pos = 41.2;
    hb.beat(); // two beats in flight, both answered 404
    hb.beat();
    await settle();
    expect(onGone).toHaveBeenCalledTimes(1);
    expect(onGone).toHaveBeenCalledWith('s1', 41.2, true);
    expect(hb.session).toBeUndefined();
    await vi.advanceTimersByTimeAsync(HEARTBEAT_MS * 3);
    expect(send).toHaveBeenCalledTimes(2);
    expect(onGone).toHaveBeenCalledTimes(1);
  });

  it('a 410 (stopped on purpose) calls onStopped once, never onGone, and stops beating', async () => {
    const { player, send, onGone, onStopped, hb } = setup(() =>
      Promise.reject(new ApiError(410, 'stopped', undefined, { error: 'stopped', stoppedBy: 'another device' })),
    );
    hb.start('s1');
    player.pos = 33;
    hb.beat();
    hb.beat();
    await settle();
    expect(onStopped).toHaveBeenCalledTimes(1);
    expect(onStopped).toHaveBeenCalledWith('s1', 33);
    expect(onGone).not.toHaveBeenCalled();
    expect(hb.session).toBeUndefined();
    await vi.advanceTimersByTimeAsync(HEARTBEAT_MS * 15); // past the server's two-minute memory of the stop
    expect(send).toHaveBeenCalledTimes(2);
  });

  it('a 410 for a session it has moved on from is ignored', async () => {
    let fail!: (e: unknown) => void;
    const { onStopped, hb } = setup((n) => (n === 1 ? new Promise((_, rej) => (fail = rej)) : Promise.resolve()));
    hb.start('s1');
    hb.beat();
    hb.start('s2');
    fail(new ApiError(410, 'stopped'));
    await settle();
    expect(onStopped).not.toHaveBeenCalled();
    expect(hb.session).toBe('s2');
  });

  it('a 404 for a session it has moved on from is ignored', async () => {
    let fail!: (e: unknown) => void;
    const { send, onGone, hb } = setup((n) => (n === 1 ? new Promise((_, rej) => (fail = rej)) : Promise.resolve()));
    hb.start('s1');
    hb.beat();
    hb.start('s2');
    fail(new ApiError(404, 'session not found'));
    await settle();
    expect(onGone).not.toHaveBeenCalled();
    expect(hb.session).toBe('s2');
    await vi.advanceTimersByTimeAsync(HEARTBEAT_MS);
    expect(send).toHaveBeenLastCalledWith('s2', 12.5, false);
  });

  it('a network error or a 5xx does not stop it', async () => {
    const errors = [new ApiError(0, 'Cannot reach the ply server'), new ApiError(503, 'busy'), new TypeError('x')];
    const { send, onGone, hb } = setup((n) => (n <= errors.length ? Promise.reject(errors[n - 1]) : Promise.resolve()));
    hb.start('s1');
    await vi.advanceTimersByTimeAsync(HEARTBEAT_MS * 4);
    expect(send).toHaveBeenCalledTimes(4);
    expect(onGone).not.toHaveBeenCalled();
    expect(hb.session).toBe('s1');
  });

  it('never sends a position the server would refuse', () => {
    const { player, send, hb } = setup();
    hb.start('s1');
    for (const pos of [NaN, -3, Infinity]) {
      player.pos = pos;
      hb.beat();
    }
    expect(send.mock.calls.map(([, p]) => p)).toEqual([0, 0, 0]);
  });
});
