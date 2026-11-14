<script lang="ts">
  // The grid's placeholders while its list loads: the shape the cards will take.
  let { tiles = 8 }: { tiles?: number } = $props();
</script>

<div class="grid" aria-busy="true">
  {#each Array(tiles) as _, i (i)}<div class="skeleton sk"></div>{/each}
</div>

<style>
  .sk {
    aspect-ratio: 16 / 9;
  }
</style>
