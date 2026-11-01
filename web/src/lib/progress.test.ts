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
  const { v, p } = setup();
  p.begin(0);
  v.load('hls', 0);
  v.currentTime = 40;
  p.update();
  p.report(true);
  p.end(); // pagehide -> leave()
  v.unload();
  expect(p.position()).toBe(40);
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
