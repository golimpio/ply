<script lang="ts">
  import { ensureRatingPrefs, ratingPrefs } from '../lib/ratingprefs.svelte';
  import { orderShown, type RatingLike } from '../lib/ratings';
  import { app } from '../lib/state.svelte';
  import Rating from './Rating.svelte';

  // The row of rating chips under a title: the sources the settings show, in their order. A movie
  // card's rating (the page's first paint) has no link, votes or age: its chip goes without them.
  let { ratings }: { ratings: RatingLike[] } = $props();

  // The settings are asked for only when there is something to order (once per library version).
  $effect(() => {
    void app.libraryVersion;
    if (ratings.length) void ensureRatingPrefs();
  });

  const list = $derived(orderShown(ratings, ratingPrefs.shown));
  const now = new Date();
</script>

{#if list.length}
  <ul class="ratings" aria-label="Ratings">
    {#each list as entry (entry.source)}
      <li><Rating {entry} {now} /></li>
    {/each}
  </ul>
{/if}

<style>
  .ratings {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--chip-gap);
  }
</style>
