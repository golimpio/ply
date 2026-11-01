<script lang="ts">
  import { api, type ItemSummary, type Stream } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import {
    channelLabel,
    codecLabel,
    episodeLabel,
    formatBitrate,
    formatBytes,
    formatClock,
    formatDuration,
    methodLabel,
  } from '../lib/format';
  import { ensureLibrary, findItem, library } from '../lib/library.svelte';
  import { browserProfile } from '../lib/profile';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import Badges from '../components/Badges.svelte';
  import Hero from '../components/Hero.svelte';
  import Icon from '../components/Icon.svelte';

  let { id }: { id: number } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

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

  // The cached summary paints the hero at once (and lands the card morph);
  // the full detail fills in the technical section when it arrives.
  const item = $derived<ItemSummary | undefined>(detail.data ?? findItem(id));
  const isEpisode = $derived(item?.kind === 'episode');
  const resumeAt = $derived(item && progressOf(item) > 0 ? (item.progress?.positionSec ?? 0) : 0);

  // Previous / next episode of the same show.
  const siblings = $derived.by(() => {
    if (!item || item.kind !== 'episode') return { prev: undefined, next: undefined };
    const eps = library.items
      .filter((i) => i.kind === 'episode' && i.show === item.show)
      .sort((a, b) => (a.season ?? 0) - (b.season ?? 0) || (a.episode ?? 0) - (b.episode ?? 0));
    const i = eps.findIndex((e) => e.id === item.id);
    return { prev: i > 0 ? eps[i - 1] : undefined, next: i >= 0 ? eps[i + 1] : undefined };
  });

  useAmbient(() => artOf(item)?.color);

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
    return parts.join(', ');
  }

  function streamFlags(s: Stream): string {
    return [s.default && 'default', s.forced && 'forced'].filter(Boolean).join(', ');
  }

  const epTitle = (e: ItemSummary) => e.episodeTitle || `Episode ${e.episode ?? ''}`.trim();
</script>

{#if !item && detail.error}
  <div class="page center">
    <p class="error">{detail.error}</p>
    <p><a href={href.home}>Back to the library</a></p>
  </div>
{:else if !item}
  <div class="skeleton hero-sk" aria-busy="true"></div>
{:else}
  <Hero {item} label={item.show || item.title} landing>
    {#if isEpisode && item.show}
      <a class="show link" href={href.show(item.show, item.season)}>{item.show}</a>
      <h1>{epTitle(item)}</h1>
    {:else}
      <h1>{item.title}</h1>
    {/if}
    <div class="meta">
      {#if isEpisode}<span>{episodeLabel(item.season, item.episode)}</span>{/if}
      {#if item.year}<span>{item.year}</span>{/if}
      {#if item.durationSec}<span>{formatDuration(item.durationSec)}</span>{/if}
      <span class="badges"><Badges {item} /></span>
    </div>
    <div class="actions">
      {#if resumeAt}
        <a class="btn primary" href={href.play(item.id, resumeAt)}>
          <Icon name="play" />Resume from {formatClock(resumeAt)}
        </a>
        <a class="btn" href={href.play(item.id)}>Play from start</a>
      {:else}
        <a class="btn primary" href={href.play(item.id)}><Icon name="play" />Play</a>
      {/if}
    </div>
  </Hero>

  <div class="body">
    {#if siblings.prev || siblings.next}
      <nav class="siblings" aria-label="Other episodes">
        {#if siblings.prev}
          <a href={href.item(siblings.prev.id)}>
            <span class="muted">Previous</span>
            <span>{episodeLabel(siblings.prev.season, siblings.prev.episode)} {epTitle(siblings.prev)}</span>
          </a>
        {/if}
        {#if siblings.next}
          <a class="next" href={href.item(siblings.next.id)}>
            <span class="muted">Next</span>
            <span>{episodeLabel(siblings.next.season, siblings.next.episode)} {epTitle(siblings.next)}</span>
          </a>
        {/if}
      </nav>
    {/if}

    <section class="playback">
      <h2>In this browser</h2>
      {#if decision.error}
        <p class="error">{decision.error}</p>
      {:else if !decision.data}
        <p class="muted">Checking what this browser can play…</p>
      {:else}
        {@const d = decision.data}
        <p class="decision">
          <span class="badge method {d.method}">{methodLabel(d.method)}</span>
          <span class="muted">
            {d.method === 'direct'
              ? 'Plays the file as it is, with no conversion.'
              : `Video is ${d.videoAction === 'copy' ? 'passed through' : 're-encoded'}, audio is ${
                  d.audioAction === 'copy' ? 'passed through' : 're-encoded'
                }.`}
          </span>
        </p>
      {/if}
    </section>

    <details class="tech">
      <summary>Technical details</summary>
      {#if decision.data?.reasons.length}
        <h3>Why this playback method</h3>
        <ul class="reasons">
          {#each decision.data.reasons as reason}<li>{reason}</li>{/each}
        </ul>
      {/if}
      {#if detail.data}
        {@const d = detail.data}
        <h3>File</h3>
        <dl class="kv">
          <dt>Name</dt><dd class="wrap">{d.path}</dd>
          <dt>Container</dt><dd>{d.container}</dd>
          <dt>Video</dt><dd>{[codecLabel(d.videoCodec), d.width ? `${d.width}×${d.height}` : ''].filter(Boolean).join(', ')}</dd>
          <dt>Audio</dt><dd>{codecLabel(d.audioCodec)}</dd>
          <dt>Duration</dt><dd>{formatDuration(d.durationSec) || 'Unknown'}</dd>
          <dt>Size</dt><dd>{formatBytes(d.size) || 'Unknown'}</dd>
          <dt>Bitrate</dt><dd>{formatBitrate(d.bitrate) || 'Unknown'}</dd>
        </dl>

        <h3>Streams</h3>
        <div class="scroll">
          <table class="table">
            <thead>
              <tr><th>#</th><th>Type</th><th>Codec</th><th>Details</th><th>Language</th><th>Title</th><th>Flags</th></tr>
            </thead>
            <tbody>
              {#each d.streams as s (s.index)}
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

        {#if d.subtitles.length}
          <h3>Subtitles</h3>
          <ul class="reasons">
            {#each d.subtitles as sub (sub.key)}
              <li>
                {sub.title || sub.language || sub.key}
                <span class="muted">{sub.format}{sub.image ? ', image (burned in when selected)' : ''}</span>
              </li>
            {/each}
          </ul>
        {/if}
      {:else if detail.error}
        <p class="error">{detail.error}</p>
      {:else}
        <p class="muted">Loading file details…</p>
      {/if}
    </details>
  </div>
{/if}

<style>
  .hero-sk {
    height: min(68vh, 56vw);
    border-radius: 0;
  }
  .show {
    font-weight: 650;
    font-size: 1.05rem;
    width: fit-content;
  }
  .badges {
    display: inline-flex;
    gap: 4px;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    margin-top: 0.4rem;
  }
  .body {
    padding: 0.5rem var(--gutter) 4rem;
    max-width: 60rem;
    display: grid;
    gap: 2rem;
  }
  .siblings {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
  }
  .siblings a {
    display: grid;
    gap: 0.1rem;
    padding: 0.85rem 1rem;
    background: var(--bg-2);
    border-radius: var(--radius);
  }
  .siblings a:hover {
    background: var(--bg-3);
  }
  .siblings .next {
    grid-column: 2;
    text-align: right;
  }
  .siblings span:last-child {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .playback h2 {
    margin-bottom: 0.6rem;
  }
  .decision {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem 0.9rem;
    margin: 0;
  }
  .tech {
    border-top: 1px solid var(--line);
    padding-top: 1rem;
  }
  .tech summary {
    cursor: pointer;
    font-weight: 650;
    width: fit-content;
  }
  .tech h3 {
    margin: 1.5rem 0 0.6rem;
    font-size: 1rem;
  }
  .reasons {
    margin: 0;
    padding-left: 1.2rem;
    color: var(--muted);
  }
  .reasons .muted {
    margin-left: 0.6rem;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.35rem 1.5rem;
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
  @media (max-width: 600px) {
    .siblings {
      grid-template-columns: 1fr;
    }
    .siblings .next {
      grid-column: 1;
    }
  }
</style>
