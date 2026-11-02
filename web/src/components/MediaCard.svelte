<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { posterImg, wideImg, type Img } from '../lib/meta';
  import { morph } from '../lib/nav';
  import { progressOf } from '../lib/ui-types';
  import Badges from './Badges.svelte';
  import Picture from './Picture.svelte';

  // A card: picture, title under it, an optional second line, and a
  // watch-progress bar when the item has been started. `wide` is 16:9 (frame
  // grab, still or backdrop), `poster` is 2:3; `img` overrides the picture
  // (a show's poster, a title's). `note` is a short flag over the picture.
  let {
    item = undefined,
    img = undefined,
    shape = 'wide',
    href,
    title,
    sub = '',
    label = '',
    note = '',
  }: {
    item?: ItemSummary;
    img?: Img | null;
    shape?: 'wide' | 'poster';
    href: string;
    title: string;
    sub?: string | (string | number | null | undefined)[];
    label?: string;
    note?: string;
  } = $props();

  const progress = $derived(item ? progressOf(item) : 0);
  const parts = $derived((Array.isArray(sub) ? sub : [sub]).filter((p) => p !== '' && p != null));
  const picture = $derived(img !== undefined ? img : shape === 'poster' ? posterImg(item) : wideImg(item));
</script>

<a class="card {shape}" {href} onclick={morph}>
  <div class="frame">
    <Picture
      img={picture}
      ratio={shape === 'poster' ? '2 / 3' : '16 / 9'}
      sizes={shape === 'poster' ? '(max-width: 600px) 40vw, 200px' : '(max-width: 600px) 70vw, 320px'}
      label={label || title}
    />
    {#if item}<div class="badges"><Badges {item} notable /></div>{/if}
    {#if note}<span class="note">{note}</span>{/if}
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
  .card.poster {
    contain-intrinsic-size: auto 340px;
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
  .note {
    position: absolute;
    top: 8px;
    right: 8px;
    padding: 0.15rem 0.5rem;
    border-radius: 999px;
    background: var(--accent);
    color: var(--on-accent);
    font-size: 0.75rem;
    font-weight: 700;
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
  /* The last part (a character, an episode title) gives way, with an ellipsis. */
  .sub span {
    flex: none;
  }
  .sub span:last-child {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
