<script lang="ts">
  import { api, type Device } from '../../lib/api';
  import { codeDeadline, codeLeft, deviceLine } from '../../lib/devices';
  import { resource } from '../../lib/resource.svelte';

  // Settings → Devices (docs/M11-CLIENTS.md, "Pairing"): the household's devices, a code to pair a
  // new one with, and Revoke, which takes a paired device's token away. While a code is shown the
  // list is read again every few seconds: the device that pairs with it appears, and the code,
  // spent, goes.
  const RELOAD_EVERY = 3;

  let version = $state(0);
  const devices = resource(() => {
    void version;
    return api.devices();
  });
  /** The code shown, when it runs out by this page's clock, and when each device was paired before it. */
  let shown = $state<{ code: string; deadline: number; pairedBefore: Map<string, string | undefined> } | null>(null);
  let now = $state(Date.now());
  let busy = $state(false);
  let note = $state<{ ok: boolean; text: string } | null>(null);
  const left = $derived(shown ? codeLeft(shown.deadline, now) : '');
  const pairedAt = (list: Device[] | undefined) => new Map((list ?? []).map((d) => [d.id, d.paired ? d.pairedAt : undefined]));

  $effect(() => {
    if (!shown) return;
    let ticks = 0;
    const timer = setInterval(() => {
      now = Date.now();
      if (++ticks % RELOAD_EVERY === 0) version++;
      if (!codeLeft(shown!.deadline, now)) {
        shown = null;
        version++;
      }
    }, 1000);
    return () => clearInterval(timer);
  });

  // A device that pairs while the code is shown (a new one, or one paired again) has spent it.
  $effect(() => {
    const before = shown?.pairedBefore;
    const fresh = before && devices.data?.find((d) => d.paired && d.pairedAt !== before.get(d.id));
    if (fresh) {
      shown = null;
      note = { ok: true, text: `${fresh.name} is paired.` };
    }
  });

  const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

  async function showCode() {
    busy = true;
    note = null;
    try {
      const c = await api.pairingCode();
      now = Date.now();
      shown = { code: c.code, deadline: codeDeadline(c.expiresIn, now), pairedBefore: pairedAt(devices.data) };
    } catch (e) {
      note = { ok: false, text: message(e) };
    } finally {
      busy = false;
    }
  }

  async function revoke(d: Device) {
    busy = true;
    note = null;
    try {
      await api.revokeDevice(d.id);
      note = { ok: true, text: `${d.name} can no longer use ply, nor the links it was given, until it is paired again.` };
      version++;
    } catch (e) {
      note = { ok: false, text: message(e) };
    } finally {
      busy = false;
    }
  }
</script>

<section aria-labelledby="devices-h">
  <h2 id="devices-h">Devices</h2>
  <p class="muted lead">
    The phones, tablets and TVs that use ply. A paired device has a token of its own: revoking it stops that device,
    the links it was given and its live updates at once (a file already being sent finishes), and leaves everyone
    else as they are.
  </p>

  <div class="settings-panel" aria-labelledby="pair-h">
    <h3 id="pair-h">Pair a device</h3>
    {#if shown && left}
      <p class="code" aria-live="polite">{shown.code}</p>
      <p class="muted">Type this code into the app. It works once, for {left}.</p>
    {:else}
      <p class="muted">Show a code to type into the app. It works once, for five minutes.</p>
    {/if}
    <div>
      <button class="btn pill" disabled={busy} onclick={showCode}>{shown ? 'New code' : 'Show a code'}</button>
    </div>
  </div>

  {#if devices.error}
    <p class="error">{devices.error}</p>
  {:else if !devices.data}
    <div class="skeleton block" aria-busy="true"></div>
  {:else if devices.data.length === 0}
    <p class="muted">No device has said who it is yet.</p>
  {:else}
    <ul class="settings-list" aria-label="Devices">
      {#each devices.data as d (d.id)}
        <li class="settings-panel device">
          <div>
            <h3>
              {d.name}
              {#if d.paired}<span class="badge">Paired</span>{/if}
            </h3>
            <p class="muted">{deviceLine(d, now)}</p>
          </div>
          {#if d.paired}
            <button class="btn pill" disabled={busy} onclick={() => revoke(d)}>Revoke</button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  <p class="muted" class:error={!!note && !note.ok} class:ok={note?.ok === true} role="status">{note?.text ?? ''}</p>
</section>

<style>
  .device {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--settings-row-gap);
  }
  .code {
    font-family: var(--font-mono);
    font-size: var(--fs-2xl);
    letter-spacing: var(--tracking-widest);
  }
</style>
