<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { hdrLabel, resolutionLabel } from '../lib/format';

  // notable: only what sets a file apart (4K, HDR), for cards; otherwise every resolution.
  let { item, notable = false }: { item: Pick<ItemSummary, 'width' | 'height' | 'hdr'>; notable?: boolean } =
    $props();

  const res = $derived(resolutionLabel(item.width, item.height));
  const hdr = $derived(hdrLabel(item.hdr));
</script>

{#if res && (!notable || res === '4K')}<span class="badge">{res}</span>{/if}
{#if hdr}<span class="badge hdr">{hdr}</span>{/if}
