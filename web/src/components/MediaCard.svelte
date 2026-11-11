<script lang="ts" module>
  import type { CardRating, ItemSummary } from '../lib/api';
  import type { PictureSource } from '../lib/meta';

  /** What a card reads of its item: a movie card (lib/api MovieCard) and a full row both fit. */
  export type CardItem = PictureSource &
    Pick<ItemSummary, 'width' | 'height' | 'hdr' | 'durationSec' | 'progress' | 'poster' | 'watched'>;
</script>

<script lang="ts">
  import { posterImg, wideImg, type Img } from '../lib/meta';
  import { morph } from '../lib/nav';
  import { progressOf } from '../lib/ui-types';
  import { itemBadge, type WatchBadge as Watch } from '../lib/watched';
  import Badges from './Badges.svelte';
  import Picture from './Picture.svelte';
  import Rating from './Rating.svelte';
  import WatchBadge from './WatchBadge.svelte';

  // A card: picture, title under it, an optional second line, and a
  // watch-progress bar when the item has been started. `wide` is 16:9 (frame
  // grab, still or backdrop), `poster` is 2:3; `img` overrides the picture
  // (a show's poster, a title's). `note` is a short flag over the picture. `rating` is the title's
  // primary rating, shown quietly at the start of the second line. A virtual grid passes the
  // picture's load `priority`, its `sizes` (the width of its columns) and whether to `hold` it.
  // `watch` is the watch badge in the top corner (a show's "3 left"); by default the item's ✓.
  // Both badges sit over the picture: the card is as tall with them as without.
  let {
    item = undefined,
    img = undefined,
    shape = 'wide',
    href,
    title,
    sub = '',
    label = '',
    note = '',
    rating = null,
    priority = undefined,
    sizes = undefined,
    hold = false,
    watch = undefined,
  }: {
    item?: CardItem;
    img?: Img | null;
    shape?: 'wide' | 'poster';
    href: string;
    title: string;
    sub?: string | (string | number | null | undefined)[];
    label?: string;
    note?: string;
    rating?: CardRating | null;
    priority?: 'high' | 'low';
    sizes?: string;
    hold?: boolean;
    watch?: Watch | null;
  } = $props();
  const badge = $derived(watch === undefined ? itemBadge(item) : watch);

  const progress = $derived(item ? progressOf(item) : 0);
  const parts = $derived((Array.isArray(sub) ? sub : [sub]).filter((p) => p !== '' && p != null));
  const picture = $derived(img !== undefined ? img : shape === 'poster' ? posterImg(item) : wideImg(item));
</script>

<a class="card {shape}" {href} onclick={morph}>
  <div class="frame">
    <Picture
      img={picture}
      ratio={shape === 'poster' ? '2 / 3' : '16 / 9'}
      sizes={sizes ?? (shape === 'poster' ? '(max-width: 600px) 40vw, 200px' : '(max-width: 600px) 70vw, 320px')}
      label={label || title}
      {priority}
      {hold}
    />
    {#if item}<div class="badges"><Badges {item} notable /></div>{/if}
    {#if note || badge}
      <div class="corner">
        {#if badge}<WatchBadge {badge} />{/if}
        {#if note}<span class="note">{note}</span>{/if}
      </div>
    {/if}
    {#if progress > 0}
      <div class="progress" role="progressbar" aria-label="Watched" aria-valuenow={Math.round(progress * 100)}>
        <span style:width="{progress * 100}%"></span>
      </div>
    {/if}
  </div>
  <div class="title">{title}</div>
  <!-- Always there, one line tall, a rating glyph or not: every card is as tall as the next (a
       virtual grid places its rows by one measured height). -->
  <div class="sub">
    {#if rating}<span class="rating"><Rating entry={rating} compact /></span>{/if}
    {#each parts as p, i (i)}<span>{p}</span>{/each}
  </div>
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
    box-shadow: var(--ring-hairline);
  }
  .frame::after {
    content: '';
    position: absolute;
    inset: 0;
    border-radius: inherit;
    box-shadow: inset 0 0 0 2px transparent;
    transition: box-shadow var(--motion-hover);
  }
  .card:hover .frame::after {
    box-shadow: var(--ring-hover-inset);
  }
  .card:focus-visible {
    outline: none;
  }
  .card:focus-visible .frame::after {
    box-shadow: var(--ring-focus-inset);
  }
  .badges {
    position: absolute;
    top: 8px;
    left: 8px;
    display: flex;
    gap: 4px;
  }
  .corner {
    position: absolute;
    top: 8px;
    right: 8px;
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .note {
    padding: 0.15rem 0.5rem;
    border-radius: var(--radius-pill);
    background: var(--accent);
    color: var(--on-accent);
    font-size: var(--fs-2xs-2);
    font-weight: var(--fw-bold);
  }
  .progress {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 3px;
    background: var(--progress-track);
  }
  .progress span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .title {
    margin-top: 0.55rem;
    font-family: var(--font-display);
    font-size: var(--fs-lg);
    font-weight: var(--fw-regular);
    line-height: var(--lh-tight);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* One line tall: a rating glyph (1.3em) no longer makes it taller, its text where it was. */
  .sub {
    display: flex;
    height: 1lh;
    gap: 0.65rem;
    margin-top: 0.1rem;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    font-variant-numeric: tabular-nums;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* The last part (a character, an episode title) gives way, with an ellipsis. */
  .sub span {
    flex: none;
  }
  .rating {
    color: var(--text);
  }
  .sub span:last-child {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
