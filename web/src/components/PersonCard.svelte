<script lang="ts">
  import { profileImg } from '../lib/meta';
  import { href } from '../lib/router.svelte';
  import Picture from './Picture.svelte';

  // A person in a cast or crew rail: a round portrait, the name, and the part played (or the
  // job done), as the iOS app shows them. Opens the person's page; without an id (a title
  // outside the library names people the library doesn't have) it is only a card.
  let { id, name, profile, role = '' }: { id?: number; name: string; profile?: string; role?: string } = $props();
</script>

<svelte:element this={id ? 'a' : 'div'} class="person" href={id ? href.person(id) : undefined}>
  <div class="frame">
    <Picture img={profileImg({ profile })} ratio="1 / 1" label={name} sizes="(max-width: 600px) 30vw, 150px" />
  </div>
  <div class="name">{name}</div>
  {#if role}<div class="role">{role}</div>{/if}
</svelte:element>

<style>
  .person {
    display: block;
    min-width: 0;
    text-align: center;
  }
  .frame {
    border-radius: var(--radius-round);
    overflow: hidden;
    box-shadow: var(--ring-hairline);
    transition: box-shadow var(--motion-hover);
  }
  a.person:hover .frame {
    box-shadow: var(--ring-hover);
  }
  .person:focus-visible {
    outline: none;
  }
  .person:focus-visible .frame {
    box-shadow: var(--ring-focus);
  }
  .frame :global(.tile) {
    font-size: var(--fs-2xl);
  }
  .name {
    margin-top: 0.65rem;
    font-weight: var(--fw-semibold-2);
    font-size: var(--fs-sm-4);
    line-height: var(--lh-snug);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .role {
    color: var(--text-muted);
    font-size: var(--fs-xs-2);
    line-height: var(--lh-snug-2);
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>
