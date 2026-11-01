<script lang="ts">
  import { api, type Stream } from '../lib/api';
  import {
    channelLabel,
    codecLabel,
    episodeLabel,
    formatBitrate,
    formatBytes,
    formatDuration,
    methodLabel,
  } from '../lib/format';
  import { browserProfile } from '../lib/profile';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import Badges from '../components/Badges.svelte';
  import Tile from '../components/Tile.svelte';

  let { id }: { id: number } = $props();

  const detail = resource(() => {
    void app.libraryVersion;
    return api.item(id);
  });

  // The decision for THIS browser: POST the measured profile.
  const decision = resource(async () => {
    void app.libraryVersion;
    const p = await api.playback(id, { custom: browserProfile() });
    // We only want the decision; do not leave a server-side session behind.
    if (p.sessionId) void api.deleteSession(p.sessionId);
    return p;
  });

  const transfer: Record<string, string> = { smpte2084: 'PQ (HDR10)', 'arib-std-b67': 'HLG' };

  function streamDetails(s: Stream): string {
    const parts: string[] = [];
    if (s.type === 'video') {
      if (s.width && s.height) parts.push(`${s.width}×${s.height}`);
      if (s.profile) parts.push(s.profile);
      if (s.pixFmt) parts.push(s.pixFmt);
      if (s.colorTransfer && transfer[s.colorTransfer]) parts.push(transfer[s.colorTransfer]);
    } else if (s.type === 'audio') {
      if (s.channels) parts.push(channelLabel(s.channels));
      if (s.profile) parts.push(s.profile);
    }
    return parts.join(' · ');
  }

  function streamFlags(s: Stream): string {
    return [s.default && 'default', s.forced && 'forced'].filter(Boolean).join(', ');
  }
</script>

{#if detail.error}
  <p class="error">{detail.error}</p>
{:else if !detail.data}
  <p class="muted">Loading…</p>
{:else}
  {@const item = detail.data}
  <p>
    {#if item.kind === 'episode' && item.show}
      <a href={href.show(item.show)}>&larr; {item.show}</a>
    {:else}
      <a href={href.movies}>&larr; Movies</a>
    {/if}
  </p>

  <div class="hero">
    <div class="art"><Tile title={item.show || item.title} /></div>
    <div class="info">
      {#if item.kind === 'episode'}
        <div class="muted">{item.show} · {episodeLabel(item.season, item.episode)}</div>
        <h1>{item.episodeTitle || item.title}</h1>
      {:else}
        <h1>{item.title}{#if item.year}&nbsp;<span class="muted">({item.year})</span>{/if}</h1>
      {/if}
      <div class="chips">
        {#if item.durationSec}<span class="muted">{formatDuration(item.durationSec)}</span>{/if}
        <Badges {item} />
        <span class="badge">{item.container}</span>
      </div>
      <p><a class="btn primary" href={href.play(item.id)}>&#9654; Play</a></p>
    </div>
  </div>

  <section>
    <h2>Playback in this browser</h2>
    {#if decision.error}
      <p class="error">{decision.error}</p>
    {:else if !decision.data}
      <p class="muted">Checking…</p>
    {:else}
      {@const d = decision.data}
      <p>
        <span class="badge method {d.method}">{methodLabel(d.method)}</span>
        <span class="muted">video: {d.videoAction} · audio: {d.audioAction}</span>
      </p>
      {#if d.reasons.length}
        <ul class="reasons">
          {#each d.reasons as reason}<li>{reason}</li>{/each}
        </ul>
      {:else if d.method === 'direct'}
        <p class="muted">The file plays natively; no conversion needed.</p>
      {/if}
    {/if}
  </section>

  <section>
    <h2>File</h2>
    <dl class="kv">
      <dt>Name</dt><dd class="wrap">{item.path}</dd>
      <dt>Container</dt><dd>{item.container}</dd>
      <dt>Video</dt><dd>{[codecLabel(item.videoCodec), item.width ? `${item.width}×${item.height}` : ''].filter(Boolean).join(' · ')}</dd>
      <dt>Audio</dt><dd>{codecLabel(item.audioCodec)}</dd>
      <dt>Duration</dt><dd>{formatDuration(item.durationSec) || '—'}</dd>
      <dt>Size</dt><dd>{formatBytes(item.size) || '—'}</dd>
      <dt>Bitrate</dt><dd>{formatBitrate(item.bitrate) || '—'}</dd>
    </dl>
  </section>

  <section>
    <h2>Streams</h2>
    <div class="scroll">
      <table class="table">
        <thead>
          <tr><th>#</th><th>Type</th><th>Codec</th><th>Details</th><th>Language</th><th>Title</th><th>Flags</th></tr>
        </thead>
        <tbody>
          {#each item.streams as s (s.index)}
            <tr>
              <td>{s.index}</td>
              <td>{s.type}</td>
              <td>{codecLabel(s.codec)}{#if s.codecTag && s.type === 'video'} <span class="muted">({s.codecTag})</span>{/if}</td>
              <td>{streamDetails(s)}</td>
              <td>{s.language ?? ''}</td>
              <td>{s.title ?? ''}</td>
              <td>{streamFlags(s)}</td>
            </tr>
          {:else}
            <tr><td colspan="7" class="muted">No stream information.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  {#if item.subtitles.length}
    <section>
      <h2>Subtitles</h2>
      <ul class="reasons">
        {#each item.subtitles as sub (sub.key)}
          <li>
            {sub.title || sub.language || sub.key}
            <span class="muted">· {sub.format}{sub.image ? ' (image, burn-in)' : ''}</span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}
{/if}

<style>
  .hero {
    display: flex;
    gap: 1.25rem;
    align-items: flex-start;
  }
  .art {
    flex: 0 0 150px;
  }
  .info {
    flex: 1;
    min-width: 0;
  }
  .info h1 {
    margin: 0.1rem 0 0.5rem;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
  }
  section {
    margin-top: 1.75rem;
  }
  h2 {
    margin: 0 0 0.6rem;
    font-size: 1.1rem;
  }
  .reasons {
    margin: 0.4rem 0 0;
    padding-left: 1.2rem;
    color: var(--muted);
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.35rem 1.25rem;
    margin: 0;
  }
  .kv dt {
    color: var(--muted);
  }
  .kv dd {
    margin: 0;
  }
  .wrap {
    overflow-wrap: anywhere;
  }
  .scroll {
    overflow-x: auto;
  }
  @media (max-width: 540px) {
    .hero {
      flex-direction: column;
    }
    .art {
      flex-basis: auto;
      width: 110px;
    }
  }
</style>
