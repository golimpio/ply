<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { hdrLabel, resolutionLabel } from '../lib/format';

  let { item }: { item: Pick<ItemSummary, 'width' | 'height' | 'hdr'> } = $props();

  const res = $derived(resolutionLabel(item.width, item.height));
  const hdr = $derived(hdrLabel(item.hdr));
</script>

{#if res}<span class="badge">{res}</span>{/if}
{#if hdr}<span class="badge hdr">{hdr}</span>{/if}
