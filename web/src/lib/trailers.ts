import type { Trailer } from './api';

// The keys the server stores (internal/store/videos.go, ValidVideo). Checked again here: the
// player's address is built from them, and nothing else may get into it.
const KEY = { youtube: /^[A-Za-z0-9_-]{11}$/, vimeo: /^[0-9]{1,12}$/ } as const;

/**
 * Where a trailer plays, in an <iframe>: YouTube's privacy-enhanced player (no cookies until it
 * plays) or Vimeo's with Do Not Track, starting at once (the click on Trailer is the gesture),
 * inline on an iPhone. Undefined for a trailer that is not one of those.
 */
export function embedUrl(t: Pick<Trailer, 'site' | 'key'>): string | undefined {
  if (t.site === 'youtube' && KEY.youtube.test(t.key)) {
    return `https://www.youtube-nocookie.com/embed/${t.key}?autoplay=1&rel=0&playsinline=1`;
  }
  if (t.site === 'vimeo' && KEY.vimeo.test(t.key)) {
    return `https://player.vimeo.com/video/${t.key}?autoplay=1&dnt=1`;
  }
  return undefined;
}

/** The trailers that can be played, in the server's order (best first). */
export function playable(list: readonly Trailer[]): Trailer[] {
  return list.filter((t) => embedUrl(t) !== undefined);
}

/** A trailer's name in the picker: its own, else what it is ("Trailer", "Teaser 2"). */
export function trailerLabel(t: Trailer, all: readonly Trailer[]): string {
  if (t.name?.trim()) return t.name.trim();
  const kind = t.type === 'teaser' ? 'Teaser' : 'Trailer';
  const same = all.filter((o) => !o.name?.trim() && (o.type === 'teaser') === (t.type === 'teaser'));
  return same.length > 1 ? `${kind} ${same.indexOf(t) + 1}` : kind;
}
