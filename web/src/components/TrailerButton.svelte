<script lang="ts">
  import { api, type Trailer } from '../lib/api';
  import { resource } from '../lib/resource.svelte';
  import { embedUrl, playable, trailerLabel } from '../lib/trailers';
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  // The Trailer button of a film's or a show's hero, and the player it opens. The server fetches
  // the trailers the first time a page asks; until they are in, or when there are none (or online
  // lookups are off, or the request fails), there is no button. The player is YouTube's or Vimeo's
  // own, in an <iframe> made on the click: nothing reaches those sites before it, and closing the
  // dialog removes it, which stops the video. Each trailer picked gets a new iframe: changing the src
  // of one would add an entry to the frame's history, and Back would go there instead of leaving the
  // page. A page whose title changes (a fixed match) makes a new button ({#key} there): the old
  // title's trailers must not stay while the new one's are fetched.
  let { titleId, title }: { titleId: number; title: string } = $props();

  const list = resource(() => api.trailers(titleId));
  const trailers = $derived(playable(list.data ?? []));

  let open = $state(false);
  let current = $state<Trailer>();
  const src = $derived(current && embedUrl(current));

  function play(t: Trailer) {
    current = t;
    open = true;
  }
</script>

{#if trailers.length}
  <button class="btn" aria-haspopup="dialog" onclick={() => play(trailers[0])}><Icon name="film" />Trailer</button>
  <Dialog bind:open title={current ? trailerLabel(current, trailers) : 'Trailer'} video>
    {#if src && current}
      <div class="frame">
        {#key src}
          <iframe
            {src}
            title="{trailerLabel(current, trailers)} – {title} ({current.site === 'youtube' ? 'YouTube' : 'Vimeo'})"
            allow="autoplay; encrypted-media; fullscreen; picture-in-picture"
            allowfullscreen
            referrerpolicy="strict-origin-when-cross-origin"
          ></iframe>
        {/key}
      </div>
    {/if}
    {#if trailers.length > 1}
      <ul class="chips" aria-label="Trailers">
        {#each trailers as t (t.site + t.key)}
          <li>
            <button class="chip" aria-pressed={t === current} onclick={() => (current = t)}>{trailerLabel(t, trailers)}</button>
          </li>
        {/each}
      </ul>
    {/if}
  </Dialog>
{/if}

<style>
  /* The player stays in view with the dialog's header (and the chips under it) in the window; the
     max-height carries over to the width through the aspect ratio, so it stays 16:9. Above 600 px the
     dialog's own width does this already; below it the dialog is the window's width (a small phone
     held sideways) and this is what does. */
  .frame {
    max-height: calc(88dvh - 7rem);
    margin-inline: auto;
    aspect-ratio: 16 / 9;
    background: var(--surface-video);
    border-radius: var(--radius);
    overflow: hidden;
  }

  iframe {
    display: block;
    width: 100%;
    height: 100%;
    border: 0;
  }
  .chips {
    list-style: none;
    margin: 0.9rem 0 0;
    padding: 0;
  }
</style>
