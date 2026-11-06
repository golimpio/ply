<script lang="ts">
  // The field of a key or a PIN. The server never sends a key back, so a key that is set shows as a
  // row of dots, a placeholder only: nothing of the key is in the page. What is typed replaces it.
  // new-password and the managers' opt-outs: a password manager must never fill ply's own token
  // (saved by the sign-in prompt) in here, where Save would send it to TMDB, TVDB or MDBList.
  let {
    id,
    set,
    value = $bindable(''),
    unsetPlaceholder = 'Paste your key',
    describedby,
  }: {
    id: string;
    /** A key (or PIN) is set, wherever it comes from. */
    set: boolean;
    value?: string;
    unsetPlaceholder?: string;
    describedby?: string;
  } = $props();

  /** The mask: the same eight dots whatever the key, so its length isn't given away either. */
  const MASK = '••••••••';
</script>

<input
  {id}
  class="text-field"
  bind:value
  type="password"
  autocomplete="new-password"
  data-1p-ignore
  data-lpignore="true"
  data-bwignore
  spellcheck="false"
  aria-describedby={describedby}
  placeholder={set ? MASK : unsetPlaceholder}
/>

<style>
  input {
    min-width: 0;
  }
</style>
