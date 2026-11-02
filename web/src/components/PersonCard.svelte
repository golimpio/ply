<script lang="ts">
  import { profileImg } from '../lib/meta';
  import { href } from '../lib/router.svelte';
  import Picture from './Picture.svelte';

  // A person in a cast or crew rail: portrait, name, and the part played (or
  // the job done). Opens the person's page.
  let { id, name, profile, role = '' }: { id: number; name: string; profile?: string; role?: string } = $props();
</script>

<a class="person" href={href.person(id)}>
  <div class="frame">
    <Picture img={profileImg({ profile })} ratio="2 / 3" label={name} sizes="(max-width: 600px) 30vw, 150px" />
  </div>
  <div class="name">{name}</div>
  {#if role}<div class="role">{role}</div>{/if}
</a>

<style>
  .person {
    display: block;
    min-width: 0;
  }
  .frame {
    border-radius: var(--radius);
    overflow: hidden;
    box-shadow: 0 0 0 1px rgb(255 255 255 / 0.06);
    transition: box-shadow 0.15s;
  }
  .person:hover .frame {
    box-shadow: 0 0 0 2px rgb(255 255 255 / 0.55);
  }
  .person:focus-visible {
    outline: none;
  }
  .person:focus-visible .frame {
    box-shadow: 0 0 0 3px var(--text);
  }
  .frame :global(.tile) {
    font-size: 1.3rem;
  }
  .name {
    margin-top: 0.5rem;
    font-weight: 650;
    font-size: 0.92rem;
    line-height: 1.25;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .role {
    color: var(--muted);
    font-size: 0.82rem;
    line-height: 1.3;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>
