<script lang="ts">
  import type { TitleSource } from '../lib/api';

  // Where a title's details come from, small and quiet beside the other facts.
  let { source }: { source: TitleSource | undefined } = $props();

  const LABELS: Record<TitleSource, [label: string, title: string]> = {
    tvdb: ['TVDB', 'Details from TheTVDB'],
    tmdb: ['TMDB', 'Details from TMDB'],
    nfo: ['From .nfo', 'Details from the .nfo file next to it'],
    mixed: ['TMDB and .nfo', 'Details from TMDB, with the .nfo file next to it on top'],
  };
  const label = $derived(source ? LABELS[source] : undefined);
</script>

{#if label}<span class="badge source" title={label[1]}>{label[0]}</span>{/if}
