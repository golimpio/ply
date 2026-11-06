<script lang="ts">
  import { api, imageUrl, type ReviewEntry, type TitleKind } from '../../lib/api';
  import { matchingStatus } from '../../lib/matching.svelte';
  import { href } from '../../lib/router.svelte';
  import { resource } from '../../lib/resource.svelte';
  import { app, rescan, scan } from '../../lib/state.svelte';
  import FixMatch from '../../components/FixMatch.svelte';
  import LibraryFolders from '../../components/LibraryFolders.svelte';
  import Picture from '../../components/Picture.svelte';

  // Settings → Library: the folders, what the matcher wasn't sure of, and a scan.

  // Polled while matching runs: the review list is fetched again when it settles.
  const status = matchingStatus({ poll: true });
  const review = resource(() => {
    void app.libraryVersion;
    return api.review();
  });

  // The "fix" dialog for one row of the review list.
  let fixing = $state(false);
  let fix = $state<{ kind: TitleKind; itemId?: number; show?: string; query: string; year?: number; matched: boolean }>({
    kind: 'movie',
    query: '',
    matched: false,
  });
  function openFix(r: ReviewEntry) {
    fix = {
      kind: r.kind,
      itemId: r.itemId,
      show: r.show,
      query: r.kind === 'series' ? r.show || r.title : r.title,
      year: r.year || undefined,
      matched: r.matchState !== 'unmatched', // nothing to remove from one that has no match
    };
    fixing = true;
  }

  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
</script>

<section aria-labelledby="folders-h">
  <h2 id="folders-h">Library folders</h2>
  <p class="muted lead">Each folder holds movies or TV shows, never both. Changes take effect at once.</p>
  <LibraryFolders />
  <div class="scan">
    <button class="btn" onclick={rescan} disabled={scan.running}>{scan.running ? 'Scanning…' : 'Scan the library now'}</button>
    <span class="muted">Reads every folder again, for what is new, changed or gone.</span>
  </div>
</section>

<section aria-labelledby="review-h">
  <h2 id="review-h">
    Needs attention{#if review.data?.length}<span class="count">{review.data.length}</span>{/if}
  </h2>
  {#if status.matching}
    <p class="muted matching" role="status">Matching is under way: this list is brought up to date when it is done.</p>
  {/if}
  {#if review.error}
    <p class="error">{review.error}</p>
  {:else if !review.data}
    <div class="skeleton block" aria-busy="true"></div>
  {:else if !review.data.length}
    <p class="muted">Every movie and show has a match.</p>
  {:else}
    <ul class="review">
      {#each review.data as r (`${r.kind}:${r.itemId ?? r.show}`)}
        <li>
          <div class="poster">
            <Picture img={r.candidate?.poster ? { src: imageUrl(r.candidate.poster, 'w92') } : null} ratio="2 / 3" label={r.title} />
          </div>
          <div class="text">
            <a class="title" href={r.kind === 'movie' && r.itemId ? href.item(r.itemId) : href.show(r.show ?? r.title)}>
              {r.title}{#if r.year}<span class="muted year">{r.year}</span>{/if}
            </a>
            <p class="muted">
              {#if r.matchState === 'unmatched'}
                Nothing found{r.kind === 'series' ? ' for this show' : ''}.
              {:else if r.episodes?.length}
                <!-- Flagged for its episodes: the entry can't say whether the match is also unsure. -->
                {#if r.candidate}Matched with {r.candidate.title}{r.candidate.year ? ` (${r.candidate.year})` : ''}.{/if}
              {:else if r.candidate}
                Matched with {r.candidate.title}{r.candidate.year ? ` (${r.candidate.year})` : ''}, but not for sure.
              {:else}
                Not sure about this match.
              {/if}
            </p>
            {#if r.episodes?.length}
              <details class="episodes">
                <summary>
                  {plural(r.episodes.length, 'episode')} with no episode number in the file name
                </summary>
                <ul>
                  {#each r.episodes as ep (ep.id)}<li><a class="link" href={href.item(ep.id)}><code>{ep.path.split('/').pop()}</code></a></li>{/each}
                </ul>
              </details>
            {/if}
          </div>
          <button class="btn" onclick={() => openFix(r)}>Fix</button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<FixMatch
  bind:open={fixing}
  kind={fix.kind}
  itemId={fix.itemId}
  show={fix.show}
  query={fix.query}
  year={fix.year}
  matched={fix.matched}
/>

<style>
  .scan {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem 1rem;
  }
  .scan .muted {
    font-size: var(--fs-sm);
  }
  .matching {
    margin: 0;
    font-size: var(--fs-sm);
  }
  .review {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  .review li {
    display: grid;
    grid-template-columns: 2.6rem 1fr auto;
    align-items: center;
    gap: 0.9rem;
    padding: 0.5rem 0.75rem 0.5rem 0.5rem;
    background: var(--surface-raised);
    border-radius: var(--radius);
  }
  .review .poster {
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .review .poster :global(.tile) {
    font-size: var(--fs-3xs);
  }
  .review .text {
    min-width: 0;
  }
  .review .title {
    font-weight: var(--fw-semibold-2);
  }
  .review .year {
    margin-left: 0.5rem;
    font-weight: var(--fw-medium);
  }
  .review p {
    margin: 0.1rem 0 0;
    font-size: var(--fs-sm);
  }
  .episodes {
    margin-top: 0.3rem;
    font-size: var(--fs-xs-3);
  }
  .episodes summary {
    color: var(--warn);
    cursor: pointer;
  }
  .episodes ul {
    margin: 0.35rem 0 0;
    padding-left: 1.1rem;
  }
  .episodes code {
    overflow-wrap: anywhere;
  }
</style>
