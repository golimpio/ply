<script lang="ts" module>
  import type { RatingEntry } from '../lib/api';

  /** Whether a source has a glyph of its own. */
  export const hasGlyph = (_source: RatingEntry['source']) => true;
</script>

<script lang="ts">
  import { metacriticBand, tomatoState } from '../lib/ratings';

  // Vector logos for the 7 rating systems: IMDb, Rotten Tomatoes Critics, Rotten Tomatoes Audience,
  // Metacritic, Trakt, Letterboxd, and TMDB.
  let { entry }: { entry: Pick<RatingEntry, 'source' | 'score100' | 'state'> } = $props();

  const state = $derived(tomatoState(entry));
  const band = $derived(metacriticBand(entry.score100));
</script>

{#if entry.source === 'rt_critics'}
  <svg class="glyph glyph-rt" viewBox="0 0 24 24" aria-hidden="true" data-glyph={state === 'rotten' || state === 'certified' ? state : 'fresh'}>
    {#if state === 'rotten'}
      <path class="splat" d="M12 3.5l1.9 4.2 4.3-1.8-1.4 4.5 4.2 1.9-4.3 1.8 1.5 4.4-4.4-1.7L12 21l-1.8-4.2-4.4 1.7 1.5-4.4-4.3-1.8 4.2-1.9-1.4-4.5 4.3 1.8z" />
    {:else if state === 'certified'}
      <circle class="ring" cx="12" cy="13" r="10" />
      <circle class="tomato" cx="12" cy="14" r="6" />
      <path class="leaf" d="M12 8c-.8-1.3-2.3-1.8-3.5-1.4.7 1.1 2 1.6 3.5 1.4zm0 0c.8-1.3 2.3-1.8 3.5-1.4-.7 1.1-2 1.6-3.5 1.4zm0 0V5.5" />
    {:else}
      <circle class="tomato" cx="12" cy="14" r="8.5" />
      <path class="leaf" d="M12 7.2c-1.2-1.9-3.4-2.6-5.2-2 1 1.6 2.9 2.3 5.2 2zm0 0c1.2-1.9 3.4-2.6 5.2-2-1 1.6-2.9 2.3-5.2 2zm0 0V4" />
    {/if}
  </svg>
{:else if entry.source === 'rt_audience'}
  <svg class="glyph glyph-audience" viewBox="0 0 24 24" aria-hidden="true" data-glyph={state === 'spilled' ? 'spilled' : 'upright'}>
    {#if state === 'spilled'}
      <path class="bucket" d="M2 11l11-2.5v12L2 18z" />
      <circle class="kernel" cx="16.5" cy="17.5" r="2.4" />
      <circle class="kernel" cx="20.5" cy="19.5" r="2" />
      <circle class="kernel" cx="18.5" cy="13" r="2" />
    {:else}
      <circle class="kernel" cx="8.5" cy="8.5" r="2.6" />
      <circle class="kernel" cx="12" cy="6.8" r="3" />
      <circle class="kernel" cx="15.5" cy="8.5" r="2.6" />
      <path class="bucket" d="M5.5 10h13l-1.7 11H7.2z" />
    {/if}
  </svg>
{:else if entry.source === 'letterboxd'}
  <svg class="glyph glyph-letterboxd" viewBox="0 0 24 24" aria-hidden="true" data-glyph="letterboxd">
    <circle cx="6.5" cy="12" r="5" fill="#00E054" />
    <circle cx="12" cy="12" r="5" fill="#FF8000" />
    <circle cx="17.5" cy="12" r="5" fill="#40BCF4" />
  </svg>
{:else if entry.source === 'imdb'}
  <svg class="glyph glyph-imdb" viewBox="0 0 575 290" aria-hidden="true" data-glyph="imdb">
    <rect width="575" height="290" rx="36" class="imdb-bg" />
    <g class="imdb-text">
      <!-- I -->
      <path d="M69.35 58.24h45.63v175.65H69.35z" />
      <!-- M -->
      <path d="M201.2 139.15C197.28 112.38 195.1 97.5 194.67 94.53C192.76 80.2 190.94 67.73 189.2 57.09C185.25 57.09 165.54 57.09 130.04 57.09L130.04 232.74L170.01 232.74L170.15 116.76L186.97 232.74L215.44 232.74L231.39 114.18L231.54 232.74L271.38 232.74L271.38 57.09L211.77 57.09L201.2 139.15Z" />
      <!-- D -->
      <path d="M346.71 93.63C347.21 95.87 347.47 100.95 347.47 108.89C347.47 115.7 347.47 170.18 347.47 176.99C347.47 188.68 346.71 195.84 345.2 198.48C343.68 201.12 339.64 202.43 333.09 202.43C333.09 190.9 333.09 98.66 333.09 87.13C338.06 87.13 341.45 87.66 343.25 88.7C345.05 89.75 346.21 91.39 346.71 93.63ZM367.32 230.95C372.75 229.76 377.31 227.66 381.01 224.67C384.7 221.67 387.29 217.52 388.77 212.21C390.26 206.91 391.14 196.38 391.14 180.63C391.14 174.47 391.14 125.12 391.14 118.95C391.14 102.33 390.49 91.19 389.48 85.53C388.46 79.86 385.93 74.71 381.88 70.09C377.82 65.47 371.9 62.15 364.12 60.13C356.33 58.11 343.63 57.09 321.54 57.09C319.27 57.09 307.93 57.09 287.5 57.09L287.5 232.74L342.78 232.74C355.52 232.34 363.7 231.75 367.32 230.95Z" />
      <!-- b -->
      <path d="M464.76 204.7C463.92 206.93 460.24 208.06 457.46 208.06C454.74 208.06 452.93 206.98 452.01 204.81C451.09 202.65 450.64 197.72 450.64 190C450.64 185.36 450.64 148.22 450.64 143.58C450.64 135.58 451.04 130.59 451.85 128.6C452.65 126.63 454.41 125.63 457.13 125.63C459.91 125.63 463.64 126.76 464.6 129.03C465.55 131.3 466.03 136.15 466.03 143.58C466.03 146.58 466.03 161.58 466.03 188.59C465.74 197.84 465.32 203.21 464.76 204.7ZM406.68 231.21L447.76 231.21C449.47 224.5 450.41 220.77 450.6 220.02C454.32 224.52 458.41 227.9 462.9 230.14C467.37 232.39 474.06 233.51 479.24 233.51C486.45 233.51 492.67 231.62 497.92 227.83C503.16 224.05 506.5 219.57 507.92 214.42C509.34 209.26 510.05 201.42 510.05 190.88C510.05 185.95 510.05 146.53 510.05 141.6C510.05 131 509.81 124.08 509.34 120.83C508.87 117.58 507.47 114.27 505.14 110.88C502.81 107.49 499.42 104.86 494.98 102.98C490.54 101.1 485.3 100.16 479.26 100.16C474.01 100.16 467.29 101.21 462.81 103.28C458.34 105.35 454.28 108.49 450.64 112.7C450.64 108.89 450.64 89.85 450.64 55.56L406.68 55.56L406.68 231.21Z" />
    </g>
  </svg>
{:else if entry.source === 'metacritic'}
  <svg class="glyph glyph-metacritic" viewBox="0 0 24 24" aria-hidden="true" data-glyph="metacritic" data-band={band}>
    <circle cx="12" cy="12" r="11" class="metacritic-bg" />
    <path class="metacritic-m" d="M6 16.5V9.5h1.8v1.1c.5-.8 1.3-1.3 2.3-1.3.9 0 1.6.4 2 1.1.5-.7 1.3-1.1 2.3-1.1 1.4 0 2.4.9 2.4 2.5v4.7H15v-4.2c0-.9-.4-1.4-1.1-1.4-.7 0-1.2.5-1.2 1.4v4.2h-1.8v-4.2c0-.9-.4-1.4-1.1-1.4-.7 0-1.2.5-1.2 1.4v4.2H6z"/>
  </svg>
{:else if entry.source === 'trakt'}
  <svg class="glyph glyph-trakt" viewBox="0 0 24 24" aria-hidden="true" data-glyph="trakt">
    <rect width="24" height="24" rx="5.5" class="trakt-bg" />
    <g fill="none" stroke="var(--text)" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" transform="translate(2.5, 2.5) scale(0.4)">
      <path d="M38 27A18 18 0 1 1 25 3"/>
      <path d="M11 18l13 11 17-20"/>
    </g>
  </svg>
{:else if entry.source === 'tmdb'}
  <svg class="glyph glyph-tmdb" viewBox="0 0 36 18" aria-hidden="true" data-glyph="tmdb">
    <rect width="36" height="18" rx="3.5" class="tmdb-bg" />
    <text x="18" y="12.5" font-size="9" font-weight="900" fill="var(--text)" text-anchor="middle" font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif" letter-spacing="-0.5">TMDB</text>
  </svg>
{/if}

<style>
  .glyph {
    height: 1.2em;
    width: auto;
    max-width: 2.6em;
    flex: none;
    display: inline-block;
    vertical-align: middle;
  }
  .glyph-imdb,
  .glyph-tmdb {
    height: 1.05em;
  }
  .tomato {
    fill: var(--rating-fresh);
  }
  .leaf {
    fill: var(--rating-leaf);
    stroke: var(--rating-leaf);
    stroke-width: 1.2;
    stroke-linecap: round;
  }
  .ring {
    fill: none;
    stroke: var(--rating-certified);
    stroke-width: 2;
  }
  .splat {
    fill: var(--rating-rotten);
  }
  .bucket {
    fill: var(--rating-bucket);
  }
  .kernel {
    fill: var(--rating-kernel);
  }
  .imdb-bg {
    fill: var(--warn);
  }
  .imdb-text {
    fill: var(--on-warn);
  }
  .metacritic-bg {
    fill: var(--surface-video);
    stroke: var(--warn);
    stroke-width: 1.8;
  }
  .glyph-metacritic[data-band='good'] .metacritic-bg {
    stroke: var(--rating-good);
  }
  .glyph-metacritic[data-band='mixed'] .metacritic-bg {
    stroke: var(--rating-mixed);
  }
  .glyph-metacritic[data-band='bad'] .metacritic-bg {
    stroke: var(--rating-bad);
  }
  .metacritic-m {
    fill: var(--text);
  }
  .trakt-bg {
    fill: var(--danger);
  }
  .tmdb-bg {
    fill: var(--accent);
  }
</style>
