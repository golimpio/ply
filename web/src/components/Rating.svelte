<script lang="ts">
  import { metacriticBand, ratingAriaLabel, ratingTooltip, ratingValue, safeRatingUrl, shortVotes, SOURCE_SHORT, type RatingLike } from '../lib/ratings';
  import RatingGlyph, { hasGlyph } from './RatingGlyph.svelte';

  // One rating. The chip of a detail page names its source, links to the title's page there and
  // has the votes and the age of the number in its tooltip. `compact` is the quiet form of cards
  // and rows: a glyph (or the source's short name) and the number, never a link (a card is one).
  // A movie card's rating has no link, votes or fetch time: the chip goes without them.
  let { entry, compact = false, now = new Date() }: { entry: RatingLike; compact?: boolean; now?: Date } = $props();

  const url = $derived(compact ? '' : safeRatingUrl(entry.url));
  const value = $derived(ratingValue(entry));
  const votes = $derived(!compact && entry.source === 'imdb' ? shortVotes(entry.votes) : '');
  const tooltip = $derived(ratingTooltip(entry, now));
  const spoken = $derived(
    compact ? `${ratingAriaLabel({ ...entry, votes: undefined })}.` : `${ratingAriaLabel(entry)}${url ? ' (opens in a new tab)' : ''}`,
  );
  // A Metascore's box is its own glyph; the full chip always names the source.
  const named = $derived(!compact || (!hasGlyph(entry.source) && entry.source !== 'metacritic'));
</script>

<svelte:element
  this={url ? 'a' : 'span'}
  class="score-chip"
  class:compact
  data-source={entry.source}
  href={url || undefined}
  target={url ? '_blank' : undefined}
  rel={url ? 'noreferrer' : undefined}
  title={tooltip}
>
  <span class="sr-only">{spoken}</span>
  {#if named}<span class="score-name" aria-hidden="true">{SOURCE_SHORT[entry.source]}</span>{/if}
  {#if entry.source === 'metacritic'}
    <span class="score-box" data-band={metacriticBand(entry.score100)} aria-hidden="true">{value}</span>
  {:else}
    <RatingGlyph {entry} />
    <span class="score-value" aria-hidden="true">{value}</span>
  {/if}
  {#if votes}<span class="score-votes" aria-hidden="true">{votes}</span>{/if}
</svelte:element>
