<script lang="ts">
  import { tick } from 'svelte';
  import { href, redirect, router } from '../lib/router.svelte';
  import { SETTINGS_TABS, TAB_LABELS, type SettingsTab } from '../lib/settings-tabs';
  import BackButton from '../components/BackButton.svelte';
  import Tabs from '../components/Tabs.svelte';
  import DetailsTab from './settings/DetailsTab.svelte';
  import DevicesTab from './settings/DevicesTab.svelte';
  import KeysTab from './settings/KeysTab.svelte';
  import LibraryTab from './settings/LibraryTab.svelte';
  import PlaybackTab from './settings/PlaybackTab.svelte';
  import RatingsTab from './settings/RatingsTab.svelte';
  import SchedulesTab from './settings/SchedulesTab.svelte';

  // Settings, in tabs: the tab is in the URL (#/settings?tab=keys), so a link, a reload and Back
  // find it. Switching tabs replaces the entry rather than adding one: Back leaves Settings.
  const TABS = SETTINGS_TABS.map((id) => ({ id, label: TAB_LABELS[id] }));
  // The route's tab; while the app moves to another page it keeps the one it had, so no other tab
  // mounts (and fetches) meanwhile.
  let tab = $state<SettingsTab>(router.route.name === 'settings' ? router.route.tab : 'library');
  $effect.pre(() => {
    const r = router.route;
    if (r.name === 'settings') tab = r.tab;
  });

  const select = (t: SettingsTab) => redirect(href.settingsTab(t));
  /** From a link in a panel ("API keys"): the panel goes, so the focus goes to the tab. */
  async function openKeys() {
    select('keys');
    await tick();
    document.getElementById('settings-tab-keys')?.focus();
  }
</script>

<div class="page page-container settings">
  <header>
    <BackButton />
    <h1>Settings</h1>
  </header>

  <Tabs id="settings" label="Settings" tabs={TABS} selected={tab} onselect={select}>
    {#snippet panel(t: SettingsTab)}
      <div class="panel-body">
        {#if t === 'library'}
          <LibraryTab />
        {:else if t === 'schedules'}
          <SchedulesTab onkeys={openKeys} />
        {:else if t === 'details'}
          <DetailsTab onkeys={openKeys} />
        {:else if t === 'ratings'}
          <RatingsTab onkeys={openKeys} />
        {:else if t === 'playback'}
          <PlaybackTab />
        {:else if t === 'devices'}
          <DevicesTab />
        {:else}
          <KeysTab />
        {/if}
      </div>
    {/snippet}
  </Tabs>
</div>

<style>
  .settings {
    display: grid;
    gap: 2rem;
  }
  /* What the tabs share. */
  .panel-body {
    display: grid;
    gap: 2.75rem;
  }
  .panel-body :global(section) {
    display: grid;
    gap: 1rem;
    min-width: 0;
  }
  .panel-body :global(.lead) {
    margin: 0;
    max-width: 62ch;
  }
  .panel-body :global(p.ok),
  .panel-body :global(span.ok) {
    color: var(--ok);
  }
  .panel-body :global(.warn) {
    color: var(--warn);
  }
  .panel-body :global(.block) {
    height: 9rem;
  }
  @media (max-width: 600px) {
    .settings {
      gap: 1.5rem;
      padding-bottom: 6rem;
    }
  }
</style>
