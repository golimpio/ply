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
    box-shadow: var(--ring-hairline);
    transition: box-shadow var(--motion-hover);
  }
  .person:hover .frame {
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
    margin-top: 0.5rem;
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
