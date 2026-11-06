<script lang="ts">
  let {
    serverName = '',
    error = '',
    busy = false,
    onsave,
  }: { serverName?: string; error?: string; busy?: boolean; onsave: (token: string) => void } = $props();

  let value = $state('');

  function submit(e: SubmitEvent) {
    e.preventDefault();
    const token = value.trim();
    if (token) onsave(token);
  }
</script>

<main class="gate">
  <form class="panel" onsubmit={submit}>
    <h1>ply</h1>
    <p class="muted">{serverName ? `${serverName} requires an access token.` : 'This server requires an access token.'}</p>
    <input
      type="password"
      placeholder="Access token"
      autocomplete="current-password"
      aria-label="Access token"
      bind:value
    />
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button class="btn primary" type="submit" disabled={busy || !value.trim()}>
      {busy ? 'Checking…' : 'Unlock'}
    </button>
  </form>
</main>

<style>
  .gate {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: 1rem;
  }
  .panel {
    width: min(360px, 100%);
    display: grid;
    gap: 0.9rem;
  }
  h1 {
    margin: 0;
  }
  p {
    margin: 0;
  }
  input {
    font: inherit;
    padding: 0.65rem 0.8rem;
    color: var(--text);
    background: var(--surface-raised);
    border: 1px solid var(--line);
    border-radius: var(--radius-row);
  }
  input:focus {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
</style>
