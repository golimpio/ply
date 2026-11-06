<script lang="ts">
  // The notice the TMDB API terms (paragraph 3) require, in their words: with the TMDB key, and in
  // Details, where the lookups it is about are switched on and off.
  let { class: cls = '' }: { class?: string } = $props();
</script>

<p class="muted tmdb-notice {cls}">
  This product uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by
  <a class="link" href="https://www.themoviedb.org/" target="_blank" rel="noreferrer">TMDB</a>.
</p>

<style>
  .tmdb-notice {
    margin: 0;
    font-size: var(--fs-xs-3);
  }
</style>
