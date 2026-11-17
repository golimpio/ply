import { describe, expect, it } from 'vitest';
import { messageOf } from './errors';

// What a catch holds is an Error, or anything else thrown.
describe('messageOf', () => {
  it('gives the message of an Error, ApiError included', () => {
    expect(messageOf(new Error('it failed'))).toBe('it failed');
  });

  it('gives the text of what is not an Error', () => {
    expect(messageOf('a string')).toBe('a string');
    expect(messageOf(404)).toBe('404');
    expect(messageOf(null)).toBe('null');
    expect(messageOf(undefined)).toBe('undefined');
    expect(messageOf({ error: 'an object' })).toBe('[object Object]');
  });
});
