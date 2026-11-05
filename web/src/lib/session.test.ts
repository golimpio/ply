import { expect, it } from 'vitest';
import { MAX_SESSION_RESTARTS, sessionGone, sessionStopped } from './session';

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
    ['stopped on purpose', { fatal: false, type: 'networkError', response: { code: 410 } }],
  ];
  for (const [name, data] of cases) expect(sessionGone(data), name).toBe(false);
});

it('a 410 means the session was stopped on purpose (not to be restarted), fatal yet or not', () => {
  expect(sessionStopped({ fatal: false, type: 'networkError', response: { code: 410 } })).toBe(true);
  expect(sessionStopped({ fatal: true, type: 'networkError', response: { code: 410 } })).toBe(true);
  expect(sessionStopped({ fatal: true, type: 'networkError', response: { code: 404 } })).toBe(false);
  expect(sessionStopped({ fatal: true, type: 'mediaError', response: { code: 410 } })).toBe(false);
});

it('the restart limit is small but not zero', () => {
  expect(MAX_SESSION_RESTARTS >= 1 && MAX_SESSION_RESTARTS <= 3).toBe(true);
});
