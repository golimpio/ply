import { expect, it } from 'vitest';
import { Progress, type MediaLike } from './progress';

/** A <video> as far as Progress can see it, including what unloading it does. */
class FakeVideo implements MediaLike {
  currentTime = 0;
  duration = NaN;
  readyState = 0;
  seeking = false;
  currentSrc = '';
  load(src: string, atTime = 0) {
    this.currentSrc = src;
    this.readyState = 1;
    this.currentTime = atTime;
    this.duration = 3600;
  }
  /** What release() does: pause, removeAttribute('src'), load(). The browser resets the clock to 0. */
  unload() {
    this.currentSrc = '';
    this.readyState = 0;
    this.currentTime = 0;
    this.duration = NaN;
  }
}

function setup() {
  const v = new FakeVideo();
  const sent: Array<[number, number, boolean]> = [];
  const p = new Progress(() => v, (pos, dur, ka) => sent.push([pos, dur, ka]), () => 7200);
  return { v, p, sent };
}

// W1: a direct-play stream that has not been seeked to the resume position yet.
it('nothing is reported while the stream still sits at 0 and the target is 45 s', () => {
  const { v, p, sent } = setup();
  p.begin(45);
  v.load('file', 0);
  p.update();
  p.report();
  expect(sent).toEqual([]);
  v.seeking = true;
  v.currentTime = 45;
  p.update();
  p.report();
  expect(sent, 'not while the seek is under way').toEqual([]);
  v.seeking = false;
  p.update();
  p.report();
  expect(sent).toEqual([[45, 3600, false]]);
});

// W2: unloading fires timeupdate at 0.
it('the timeupdate fired by unloading neither re-arms nor reports, and the position survives', () => {
  const { v, p, sent } = setup();
  p.begin(0);
  v.load('hls', 0);
  p.update();
  v.currentTime = 88.5;
  p.update();
  p.report();
  expect(sent).toEqual([[88.5, 3600, false]]);

  p.report(); // the same position again is not sent twice
  expect(sent.length).toBe(1);

  p.end(); // the stream fails and is torn down
  v.unload();
  p.update(); // timeupdate at 0
  p.report(true); // leaving the page
  expect(sent.length, 'no report from the unloaded element').toBe(1);
  expect(p.position(), 'Retry starts where playback failed').toBe(88.5);
});

it('a stream that failed before it got to its target retries from that target', () => {
  const { v, p, sent } = setup();
  p.begin(2700);
  v.load('hls', 0);
  p.update();
  p.end();
  v.unload();
  p.update();
  p.report(true);
  expect(sent).toEqual([]);
  expect(p.position()).toBe(2700);
});

it('a track switch keeps the position through a stream that never starts', () => {
  const { v, p, sent } = setup();
  p.begin(0);
  v.load('hls', 0);
  v.currentTime = 2700;
  p.update();
  // Switch audio: next stream from where we are, torn down before it ever loaded anything.
  const at = p.position();
  expect(at).toBe(2700);
  p.report(); // start() flushes the old stream first
  p.end();
  v.unload();
  p.begin(at);
  p.update();
  p.report(true);
  // A second switch before the first got going: still 2700.
  expect(p.position()).toBe(2700);
  expect(sent).toEqual([[2700, 3600, false]]);
});

// W4: the back/forward cache.
it('after pagehide tears the stream down, pageshow can restart from the saved position', () => {
  const { v, p, sent } = setup();
  p.begin(0);
  v.load('hls', 0);
  v.currentTime = 40;
  p.update();
  p.report(true);
  expect(sent, 'the report made while leaving asks for keepalive, so that it outlives the page').toEqual([[40, 3600, true]]);
  p.end(); // pagehide -> leave()
  v.unload();
  expect(p.position()).toBe(40);
});

// The sender decides what keepalive means (fetch's keepalive); Progress only has to hand it over as asked.
it('keepalive is passed on exactly as asked: off by default, on when leaving', () => {
  const { v, p, sent } = setup();
  p.begin(0);
  v.load('hls', 0);
  v.currentTime = 12;
  p.update();
  p.report(); // the ticker
  v.currentTime = 13;
  p.report(true); // pagehide, or the page being hidden
  v.currentTime = 14;
  p.report(false);
  v.currentTime = 15;
  p.report();
  expect(sent).toEqual([
    [12, 3600, false],
    [13, 3600, true],
    [14, 3600, false],
    [15, 3600, false],
  ]);
});

// With the target at 45 s a stream counts as having got there from 42 s (ARRIVED = 3 s before it:
// hls.js lands on a keyframe, a seek is not exact), and not a moment earlier.
it.each([
  [0, false],
  [10, false],
  [41, false],
  [41.9, false],
  [42, true],
  [43, true],
  [45, true],
  [46.5, true],
])('a stream meant to start at 45 s that sits at %f s is armed: %s', (at, armed) => {
  const { v, p, sent } = setup();
  p.begin(45);
  v.load('hls', at);
  p.update();
  p.report();
  expect(sent).toEqual(armed ? [[at, 3600, false]] : []);
  expect(p.position(), 'the next stream starts where this one is, once it counts, else at the target').toBe(armed ? at : 45);
});

it('a start from the beginning reports as soon as the stream is up', () => {
  const { v, p, sent } = setup();
  p.begin(0);
  v.load('file', 0);
  p.update();
  v.currentTime = 12;
  p.update();
  p.report();
  expect(sent).toEqual([[12, 3600, false]]);
});

it('the item duration stands in when the element has none', () => {
  const { v, p, sent } = setup();
  p.begin(0);
  v.load('hls', 0);
  v.duration = Infinity;
  v.currentTime = 5;
  p.update();
  p.report();
  expect(sent).toEqual([[5, 7200, false]]);
});
