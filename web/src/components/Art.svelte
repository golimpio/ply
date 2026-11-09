<script lang="ts" module>
  import type { ItemSummary } from '../lib/api';
  import type { PictureSource } from '../lib/meta';

  /** What Art reads of an item: a movie card (lib/api MovieCard) and a full row both fit. */
  export type ArtItem = PictureSource & Pick<ItemSummary, 'title' | 'show'>;
</script>

<script lang="ts">
  import { backdropImg, wideImg, type Img } from '../lib/meta';
  import Picture from './Picture.svelte';

  // Artwork for an item: the metadata's still or backdrop when there is one,
  // else the server's frame grab, else the generated title tile. `img`
  // overrides the choice (a show's backdrop behind its page, say).
  let {
    item,
    kind = 'thumb',
    ratio = '16 / 9',
    label = '',
    eager = false,
    img = undefined,
    sizes = undefined,
  }: {
    item: ArtItem | undefined;
    kind?: 'thumb' | 'backdrop';
    ratio?: string;
    label?: string;
    eager?: boolean;
    img?: Img | null;
    sizes?: string;
  } = $props();

  const chosen = $derived(img !== undefined ? img : kind === 'backdrop' ? backdropImg(item) : wideImg(item));
</script>

<Picture
  img={chosen}
  {ratio}
  {eager}
  sizes={sizes ?? (kind === 'backdrop' ? '100vw' : '(max-width: 600px) 70vw, 320px')}
  label={label || item?.show || item?.title || ''}
/>
