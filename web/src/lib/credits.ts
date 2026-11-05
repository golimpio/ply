// The pure logic behind the person, genre and collection pages: a person's
// credits sorted into the groups the page shows, how a title card links and
// reads, and the dates of a life.
import type { PersonCredit, TitleCard } from './api';
import { ratingScore } from './ratings';
import { href } from './router.svelte';

// ---- a person's credits -------------------------------------------------------

export type CreditGroup = 'Acting' | 'Directing' | 'Writing' | 'Other';

/** The order the groups appear in on the page. */
export const CREDIT_GROUPS: CreditGroup[] = ['Acting', 'Directing', 'Writing', 'Other'];

const DIRECTING = new Set(['director']);
const WRITING = new Set(['writer', 'screenplay', 'story', 'novel', 'creator']);

/** Which group a credit belongs to: cast is Acting, and a crew job is told apart by its name. */
export function groupOf(credit: Pick<PersonCredit, 'kind' | 'job'>): CreditGroup {
  if (credit.kind === 'cast') return 'Acting';
  const job = (credit.job ?? '').trim().toLowerCase();
  if (DIRECTING.has(job)) return 'Directing';
  if (WRITING.has(job)) return 'Writing';
  return 'Other';
}

export interface CreditEntry {
  card: TitleCard;
  /** The character played, or the job done; several of them (two jobs on one film) read "A, B". */
  detail: string;
}

export interface CreditSection {
  group: CreditGroup;
  entries: CreditEntry[];
}

/**
 * Credits as the page lists them: a section per group (empty ones left out, in
 * CREDIT_GROUPS order), a title once per group however many roles the person had
 * in it, and the newest title first. A title with no year goes last. Titles from
 * the same year keep the order the server sent them in.
 */
export function groupCredits(credits: PersonCredit[]): CreditSection[] {
  const byGroup = new Map<CreditGroup, Map<number, { card: TitleCard; details: string[] }>>();
  for (const c of credits) {
    const group = groupOf(c);
    let titles = byGroup.get(group);
    if (!titles) byGroup.set(group, (titles = new Map()));
    let entry = titles.get(c.title.id);
    if (!entry) titles.set(c.title.id, (entry = { card: c.title, details: [] }));
    const detail = ((group === 'Acting' ? c.character : c.job) ?? '').trim();
    if (detail && !entry.details.includes(detail)) entry.details.push(detail);
  }

  const sections: CreditSection[] = [];
  for (const group of CREDIT_GROUPS) {
    const titles = byGroup.get(group);
    if (!titles) continue;
    const entries = [...titles.values()]
      .map((e) => ({ card: e.card, detail: e.details.join(', ') }))
      .sort((a, b) => yearRank(b.card) - yearRank(a.card));
    sections.push({ group, entries });
  }
  return sections;
}

/** Sort key where a larger value is newer; a title with no year (0, or less) ranks lowest. */
const yearRank = (c: TitleCard) => Math.max(c.year, 0);

// ---- title cards --------------------------------------------------------------

/** Where a card goes: a film opens its file, a series its show page. undefined when neither is known. */
export function cardHref(card: Pick<TitleCard, 'kind' | 'itemId' | 'show'>): string | undefined {
  if (card.kind === 'series') return card.show ? href.show(card.show) : undefined;
  return card.itemId ? href.item(card.itemId) : undefined;
}

export type TitleSort = 'title' | 'year' | 'rating';

/**
 * A copy of the cards in the order asked for. Title keeps the order of the list
 * (the server sorts by sort title, so "The Godfather" files under G); year and
 * rating put the newest and the best first, the ones with none last, and break
 * ties in the order of the list. Rating goes by score100, so that an IMDb 8.0
 * (80) beats a Tomatometer of 75%.
 */
export function sortCards(cards: TitleCard[], sort: TitleSort): TitleCard[] {
  const out = [...cards];
  if (sort === 'year') return out.sort((a, b) => yearRank(b) - yearRank(a));
  if (sort === 'rating') return out.sort((a, b) => ratingScore(b) - ratingScore(a));
  return out;
}

/** "1999", "1999–2003" or '' for the years of some titles (unknown years ignored). */
export function yearSpan(cards: Pick<TitleCard, 'year'>[]): string {
  const years = cards.map((c) => c.year).filter((y) => y > 0);
  if (!years.length) return '';
  const first = Math.min(...years);
  const last = Math.max(...years);
  return first === last ? String(first) : `${first}–${last}`;
}

// ---- dates ----------------------------------------------------------------------

const MONTHS = [
  'January',
  'February',
  'March',
  'April',
  'May',
  'June',
  'July',
  'August',
  'September',
  'October',
  'November',
  'December',
];

interface DateParts {
  year: number;
  /** 1 to 12, or 0 when only the year is known. */
  month: number;
  /** 1 to 31, or 0 when the day is not known. */
  day: number;
}

function daysIn(year: number, month: number): number {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}

/**
 * Reads "1961-03-12", "1961-03" or "1961" (and a full timestamp, of which only
 * the date counts: no time zone can shift it). TMDB writes an unknown month or
 * day as 00. null when it is not a real date.
 */
function parseDate(s: string | undefined | null): DateParts | null {
  const m = /^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:[T ].*)?$/.exec((s ?? '').trim());
  if (!m) return null;
  const year = Number(m[1]);
  const month = Number(m[2] ?? 0);
  const day = month === 0 ? 0 : Number(m[3] ?? 0);
  if (year < 1 || month > 12 || day > (month ? daysIn(year, month) : 0)) return null;
  return { year, month, day };
}

/** "12 March 1961", "March 1961" or "1961"; '' when the date is missing or not a date. */
export function formatDate(s: string | undefined | null): string {
  const d = parseDate(s);
  if (!d) return '';
  if (!d.month) return String(d.year);
  const month = MONTHS[d.month - 1];
  return d.day ? `${d.day} ${month} ${d.year}` : `${month} ${d.year}`;
}

/**
 * The line of years under a person's name: "Born 12 March 1961" while they
 * live, "1961–2023" once they have died (with only one of the two known, "Died
 * 4 July 2023"). '' when neither date is known.
 */
export function lifeSpan(birthday: string | undefined | null, deathday: string | undefined | null): string {
  const born = parseDate(birthday);
  const died = parseDate(deathday);
  if (born && died) return `${born.year}–${died.year}`;
  if (died) return `Died ${formatDate(deathday)}`;
  if (born) return `Born ${formatDate(birthday)}`;
  return '';
}
