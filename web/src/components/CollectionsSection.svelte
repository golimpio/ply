<script lang="ts">
  import { href } from '../lib/router.svelte';

  let { collections }: { collections: { id: number; name: string }[] } = $props();

  const title = $derived(collections.length > 1 ? 'Collections' : 'Collection');
</script>

{#if collections.length}
  <section class="detail-section collections" aria-labelledby="collections-title">
    <div class="detail-section-head">
      <h2 class="detail-section-title" id="collections-title">{title}</h2>
    </div>
    <div class="collections-body">
      <ul class="collections-list">
        {#each collections as c (c.id)}
          <li>
            <a class="collection-row" href={href.collection(c.id)}>
              <span class="collection-name">{c.name}</span>
              <span class="collection-arrow" aria-hidden="true">→</span>
            </a>
          </li>
        {/each}
      </ul>
    </div>
  </section>
{/if}

<style>
  .collections {
    padding-bottom: var(--space-2000);
  }
  .collections-body {
    display: grid;
  }
  .collections-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .collection-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-1000);
    padding: var(--space-0850) 0;
    border-bottom: 1px solid var(--line);
    color: var(--text);
    text-decoration: none;
    font-size: var(--fs-base);
    font-weight: var(--fw-medium);
    transition: color var(--motion-hover);
  }
  /* The section after this one draws its own top hairline: a line under the last row doubles it. */
  .collections-list li:last-child .collection-row {
    border-bottom: none;
  }
  .collection-row:hover {
    color: var(--accent);
  }
  .collection-arrow {
    color: var(--text-muted);
    font-size: var(--fs-lg);
    transition:
      transform var(--motion-hover),
      color var(--motion-hover);
  }
  .collection-row:hover .collection-arrow {
    color: var(--accent);
    transform: translateX(4px);
  }
</style>
