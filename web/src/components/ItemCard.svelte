<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { formatDuration } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import Badges from './Badges.svelte';
  import Tile from './Tile.svelte';

  let { item }: { item: ItemSummary } = $props();

  const meta = $derived([item.year || '', formatDuration(item.durationSec)].filter(Boolean).join(' · '));
</script>

<a class="card" href={href.item(item.id)} title={item.title}>
  <div class="art">
    <Tile title={item.title} />
    <div class="badges"><Badges {item} /></div>
  </div>
  <div class="name">{item.title}</div>
  <div class="meta">{meta}</div>
</a>

<style>
  .card {
    display: block;
    color: inherit;
    text-decoration: none;
  }
  .art {
    position: relative;
    transition: transform 0.12s ease;
  }
  .card:hover .art,
  .card:focus-visible .art {
    transform: translateY(-2px);
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: var(--radius);
  }
  .badges {
    position: absolute;
    left: 6px;
    bottom: 6px;
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .name {
    margin-top: 0.5rem;
    font-weight: 600;
    line-height: 1.25;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .meta {
    color: var(--muted);
    font-size: 0.85rem;
    min-height: 1.3em;
  }
</style>
