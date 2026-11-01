import { expect, it } from 'vitest';
import { MAX_SESSION_RESTARTS, sessionGone } from './session';

it('a 404 from the server means the session is gone, fatal yet or not', () => {
  expect(sessionGone({ fatal: true, type: 'networkError', response: { code: 404 } })).toBe(true);
  expect(sessionGone({ fatal: false, type: 'networkError', response: { code: 404 } })).toBe(true);
});

it('other errors are not a dropped session', () => {
  const cases: Array<[string, Parameters<typeof sessionGone>[0]]> = [
    ['server error', { fatal: true, type: 'networkError', response: { code: 500 } }],
    ['timeout or offline (no response)', { fatal: true, type: 'networkError' }],
    ['status 0', { fatal: true, type: 'networkError', response: { code: 0 } }],
    ['media error', { fatal: true, type: 'mediaError', response: { code: 404 } }],
    ['too busy', { fatal: true, type: 'networkError', response: { code: 503 } }],
  ];
  for (const [name, data] of cases) expect(sessionGone(data), name).toBe(false);
});

it('the restart limit is small but not zero', () => {
  expect(MAX_SESSION_RESTARTS >= 1 && MAX_SESSION_RESTARTS <= 3).toBe(true);
});
