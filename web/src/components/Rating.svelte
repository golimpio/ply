<script lang="ts">
  import { ratingAriaLabel, ratingTooltip, ratingValue, safeRatingUrl, type RatingLike } from '../lib/ratings';
  import RatingGlyph from './RatingGlyph.svelte';

  // One rating: an SVG logo for the platform and its score value, with identical typography,
  // proportional scaling, and external links for detail pages. Spoken label and tooltip carry
  // full attribution and vote details for assistive tech.
  let { entry, compact = false, now = new Date() }: { entry: RatingLike; compact?: boolean; now?: Date } = $props();

  const url = $derived(compact ? '' : safeRatingUrl(entry.url));
  const value = $derived(ratingValue(entry));
  const tooltip = $derived(ratingTooltip(entry, now));
  const spoken = $derived(
    compact ? `${ratingAriaLabel({ ...entry, votes: undefined })}.` : `${ratingAriaLabel(entry)}${url ? ' (opens in a new tab)' : ''}`,
  );
  const titleText = $derived(
    entry.votes && entry.votes > 0
      ? tooltip
      : spoken.endsWith(' (opens in a new tab)')
        ? spoken.slice(0, -' (opens in a new tab)'.length)
        : spoken.endsWith('.')
          ? spoken.slice(0, -1)
          : spoken,
  );
</script>

<svelte:element
  this={url ? 'a' : 'span'}
  class="score-chip"
  class:compact
  data-source={entry.source}
  href={url || undefined}
  target={url ? '_blank' : undefined}
  rel={url ? 'noreferrer' : undefined}
  title={titleText}
>
  <span class="sr-only">{spoken}</span>
  <RatingGlyph {entry} />
  <span class="score-value" aria-hidden="true">{value}</span>
</svelte:element>
