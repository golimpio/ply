// A browser test reaches no server: a fetch to /api that it did not mock fails it. In Chrome a
// relative fetch would otherwise go to the test server, and the test would pass on whatever that
// answers. (fetch only: no browser suite uses EventSource, XHR or sendBeacon.)
import { afterEach, expect } from 'vitest';

const reached: string[] = [];
const realFetch = globalThis.fetch;
globalThis.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
  const url = new URL(input instanceof Request ? input.url : String(input), location.href);
  if (url.origin === location.origin && url.pathname.startsWith('/api/')) {
    reached.push(url.pathname + url.search);
    return Promise.reject(new TypeError(`a unit test reached the server: ${url.pathname}`));
  }
  return realFetch(input, init);
};

afterEach(() => {
  expect(reached.splice(0), 'requests to /api that the test did not mock').toEqual([]);
});
