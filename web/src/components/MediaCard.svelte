<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { morph } from '../lib/nav';
  import { progressOf } from '../lib/ui-types';
  import Art from './Art.svelte';
  import Badges from './Badges.svelte';

  // A 16:9 card: frame grab, title under it, an optional second line, and a
  // watch-progress bar when the item has been started.
  let {
    item,
    href,
    title,
    sub = '',
    label = '',
  }: {
    item: ItemSummary;
    href: string;
    title: string;
    sub?: string | (string | number | null | undefined)[];
    label?: string;
  } = $props();

  const progress = $derived(progressOf(item));
  const parts = $derived((Array.isArray(sub) ? sub : [sub]).filter((p) => p !== '' && p != null));
</script>

<a class="card" {href} onclick={morph}>
  <div class="frame">
    <Art {item} label={label || title} />
    <div class="badges"><Badges {item} notable /></div>
    {#if progress > 0}
      <div class="progress" role="progressbar" aria-label="Watched" aria-valuenow={Math.round(progress * 100)}>
        <span style:width="{progress * 100}%"></span>
      </div>
    {/if}
  </div>
  <div class="title">{title}</div>
  {#if parts.length}<div class="sub">{#each parts as p, i (i)}<span>{p}</span>{/each}</div>{/if}
</a>

<style>
  .card {
    display: block;
    min-width: 0;
    content-visibility: auto;
    contain-intrinsic-size: auto 260px;
  }
  .frame {
    position: relative;
    border-radius: var(--radius);
    overflow: hidden;
    box-shadow: 0 0 0 1px rgb(255 255 255 / 0.06);
  }
  .frame::after {
    content: '';
    position: absolute;
    inset: 0;
    border-radius: inherit;
    box-shadow: inset 0 0 0 2px transparent;
    transition: box-shadow 0.15s;
  }
  .card:hover .frame::after {
    box-shadow: inset 0 0 0 2px rgb(255 255 255 / 0.55);
  }
  .card:focus-visible {
    outline: none;
  }
  .card:focus-visible .frame::after {
    box-shadow: inset 0 0 0 3px var(--text);
  }
  .badges {
    position: absolute;
    top: 8px;
    left: 8px;
    display: flex;
    gap: 4px;
  }
  .progress {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 4px;
    background: rgb(0 0 0 / 0.5);
  }
  .progress span {
    display: block;
    height: 100%;
    background: var(--text);
  }
  .title {
    margin-top: 0.6rem;
    font-weight: 650;
    line-height: 1.25;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    display: flex;
    gap: 0.75rem;
    margin-top: 0.1rem;
    color: var(--muted);
    font-size: 0.875rem;
    font-variant-numeric: tabular-nums;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
