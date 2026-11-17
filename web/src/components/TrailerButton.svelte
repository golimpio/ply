<script lang="ts">
  import { untrack } from 'svelte';
  import { activity } from '../lib/activity.svelte';
  import { api } from '../lib/api';
  import { resource } from '../lib/resource.svelte';
  import { href } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import { buttonState } from '../lib/trailers';
  import Icon from './Icon.svelte';
  import Menu from './Menu.svelte';

  // The Trailer button of a film's or a show's hero (docs/M13-TRAILERS.md, "Clients"). A trailer
  // is a file of the library, played by ply's own player as a film is. A title with none on disk
  // has the server download one when its page asks (the request does it): the button spins until
  // the job ends on the live feed, and then plays it, or goes when the download failed. A page
  // whose title changes (a fixed match) makes a new button ({#key} there).
  let { titleId }: { titleId: number } = $props();

  let ended = $state(0); // the downloads seen to end: each asks the list again
  const list = resource(() => {
    void app.libraryVersion; // the trailer indexed is a change of the library
    void ended;
    return api.trailers(titleId);
  });
  const btn = $derived(buttonState(list.data));

  let seen = 0; // the job already acted on
  $effect(() => {
    if (btn.kind !== 'downloading' || !btn.jobId) return;
    const id = btn.jobId;
    const over = activity.snap.history.some((j) => j.id === id && (j.state === 'done' || j.state === 'failed' || j.state === 'cancelled'));
    if (over && seen !== id) {
      seen = id;
      untrack(() => ended++);
    }
  });
</script>

{#if btn.kind === 'play'}
  <a class="btn" href={href.play(btn.itemId)}><Icon name="film" />Trailer</a>
  {#if btn.choices.length > 1}
    <Menu
      label="More trailers"
      items={btn.choices.slice(1).map((c) => ({ label: c.name, run: () => (location.hash = href.play(c.itemId)) }))}
    />
  {/if}
{:else if btn.kind === 'downloading'}
  <button class="btn" disabled aria-label="Downloading trailer"><span class="spinner" aria-hidden="true"></span>Trailer</button>
{/if}
