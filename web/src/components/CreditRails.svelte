<script lang="ts">
  import type { TitleDetail } from '../lib/api';
  import { crewRail } from '../lib/meta';
  import PersonCard from './PersonCard.svelte';
  import Rail from './Rail.svelte';

  // A title's people, as the iOS app shows them: a rail of the cast, then one of the crew, each a
  // round portrait. A rail with nobody in it is not drawn (Rail's own rule).
  let { meta }: { meta: Pick<TitleDetail, 'cast' | 'crew'> | undefined | null } = $props();

  const cast = $derived((meta?.cast ?? []).slice(0, 30));
  const crew = $derived(crewRail(meta?.crew ?? [], (c) => c.personId).slice(0, 30));
</script>

<Rail title="Cast" items={cast} key={(c) => c.personId} size="person">
  {#snippet card(c)}
    <PersonCard id={c.personId} name={c.name} profile={c.profile} role={c.character} />
  {/snippet}
</Rail>
<Rail title="Crew" items={crew} key={(c) => c.personId} size="person">
  {#snippet card(c)}
    <PersonCard id={c.personId} name={c.name} profile={c.profile} role={c.jobs} />
  {/snippet}
</Rail>
