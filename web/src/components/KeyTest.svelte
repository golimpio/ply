<script lang="ts">
  import { api, type KeyProvider } from '../lib/api';

  // "Test": asks the server to use the key in use (from wherever it comes) now. The page never
  // sees the key; only whether it works, and why not.
  let {
    provider,
    name,
    busy = false,
    unavailable = '',
    about,
    onworks,
  }: {
    provider: KeyProvider;
    name: string;
    /** A save is under way: the key may be about to change. */
    busy?: boolean;
    /** Why there is nothing to test (no key, lookups off); '' when there is. */
    unavailable?: string;
    /**
     * What a result is about: the key and PIN as set, where they come from, whether lookups are on,
     * and the saves made. A change clears the result and drops an answer still on its way; the same
     * settings fetched again (a scan ending) keep it.
     */
    about: string;
    /** The key works: the server has cleared a "rejected" or "unreachable" status, which the page can now ask for. */
    onworks?: () => void;
  } = $props();

  let testing = $state(false);
  let result = $state<{ ok: boolean; text: string } | null>(null);
  let asked = 0;

  $effect(() => {
    void about;
    result = null;
    asked++; // an answer still on its way is about the key there was
  });

  async function test() {
    const mine = ++asked;
    testing = true;
    result = null;
    try {
      const r = await api.testKey(provider);
      if (mine !== asked) return;
      result = r.ok ? { ok: true, text: `The ${name} key works.` } : { ok: false, text: r.error || `${name} did not accept the key.` };
      if (r.ok) onworks?.();
    } catch (e) {
      if (mine !== asked) return;
      result = { ok: false, text: e instanceof Error ? e.message : String(e) };
    } finally {
      testing = false;
    }
  }
</script>

<div class="test">
  <button
    type="button"
    class="btn"
    disabled={busy || testing || !!unavailable}
    onclick={test}
    aria-label="Test the {name} key"
    aria-describedby={unavailable ? `${provider}-test-why` : undefined}
  >
    {testing ? 'Testing…' : 'Test'}
  </button>
  {#if unavailable}<span id="{provider}-test-why" class="muted why">{unavailable}</span>{/if}
  <p class="result" class:ok={result?.ok} class:error={result && !result.ok} role="status">{result?.text ?? ''}</p>
</div>

<style>
  .test {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem 0.9rem;
  }
  .why {
    font-size: var(--fs-sm);
  }
  .result {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .ok {
    color: var(--ok);
  }
</style>
