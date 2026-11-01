<script lang="ts">
  import { onMount } from 'svelte';
  import type HlsType from 'hls.js';
  import { api, mediaUrl, type ItemDetail, type Playback } from '../lib/api';
  import { methodLabel } from '../lib/format';
  import { browserProfile } from '../lib/profile';

  let { item, startSec = 0 }: { item: ItemDetail; startSec?: number } = $props();

  let video = $state<HTMLVideoElement>();
  let phase = $state<'starting' | 'ready' | 'error'>('starting');
  let error = $state('');
  let playback = $state<Playback>();

  // Non-reactive handles used for teardown and async-race checks.
  let el: HTMLVideoElement | undefined;
  let hls: HlsType | undefined;
  let sessionId: string | undefined;
  let destroyed = false;
  let attempt = 0;
  let resumed = false;

  const isHlsUrl = (url: string) => new URL(url).pathname.endsWith('.m3u8');

  /** Stop media, drop hls.js and end the server session (best effort, survives page unload). */
  function release() {
    hls?.destroy();
    hls = undefined;
    if (el) {
      el.pause();
      el.removeAttribute('src');
      el.load();
    }
    if (sessionId) {
      void api.deleteSession(sessionId);
      sessionId = undefined;
    }
  }

  function fail(message: string) {
    phase = 'error';
    error = message;
    release();
  }

  async function start() {
    const run = ++attempt;
    release();
    phase = 'starting';
    error = '';
    playback = undefined;
    resumed = false;

    let pb: Playback;
    try {
      pb = await api.playback(item.id, {
        custom: browserProfile(),
        startSec: startSec > 0 ? startSec : undefined,
      });
    } catch (e) {
      if (!destroyed && run === attempt) fail(e instanceof Error ? e.message : String(e));
      return;
    }
    if (destroyed || run !== attempt) {
      if (pb.sessionId) void api.deleteSession(pb.sessionId);
      return;
    }
    sessionId = pb.sessionId;
    playback = pb;

    if (!pb.url) {
      // Why it cannot be streamed is the last reason the server gave ("transcoding is not available yet").
      const why = pb.reasons[pb.reasons.length - 1];
      fail(
        pb.method === 'direct'
          ? 'The server returned no stream URL for this item.'
          : `This file needs ${pb.method} to play in this browser, which the server cannot do yet${why ? ` (${why})` : ''}.`,
      );
      return;
    }

    const url = mediaUrl(pb.url);
    const v = el!;
    if (!isHlsUrl(url) || v.canPlayType('application/vnd.apple.mpegurl')) {
      v.src = url; // direct file, or native HLS (Safari / iOS)
    } else {
      const { default: Hls } = await import('hls.js');
      if (destroyed || run !== attempt) return;
      if (!Hls.isSupported()) {
        fail('This browser cannot play HLS streams.');
        return;
      }
      // The playlist is the whole file, so a resume is just a start position: the server cuts in
      // at that segment, and later seeks anywhere are plain segment requests it answers by itself.
      const h = new Hls({
        startPosition: pb.startSec > 0 ? pb.startSec : -1,
        backBufferLength: 60,
        maxBufferLength: 40,
      });
      let networkRetries = 0;
      let mediaRetries = 0;
      h.on(Hls.Events.ERROR, (_evt, data) => {
        if (!data.fatal || h !== hls) return;
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR && networkRetries++ < 3) h.startLoad();
        else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && mediaRetries++ < 2) h.recoverMediaError();
        else fail(`Stream error: ${data.details}`);
      });
      hls = h;
      h.loadSource(url);
      h.attachMedia(v);
    }
    v.play().catch(() => {
      // autoplay blocked: the user can press play
    });
  }

  // Resume: stream time 0 is file time 0, so a resume is a plain seek once metadata is known.
  function onLoadedMetadata() {
    const at = playback?.startSec ?? 0;
    if (!resumed && el && at > 0 && Math.abs(el.currentTime - at) > 1) el.currentTime = at;
    resumed = true;
  }

  function onVideoError() {
    if (!el?.currentSrc) return; // fired by our own teardown
    const code = el.error?.code;
    const why =
      code === MediaError.MEDIA_ERR_NETWORK
        ? 'a network error interrupted the stream'
        : code === MediaError.MEDIA_ERR_DECODE
          ? 'the media could not be decoded'
          : code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED
            ? 'this format is not supported'
            : 'playback failed';
    fail(`The browser could not play this item: ${why}.`);
  }

  onMount(() => {
    el = video;
    void start();
    return () => {
      destroyed = true;
      release();
    };
  });
</script>

<svelte:window onpagehide={release} />

<div class="player">
  <div class="frame">
    <!-- svelte-ignore a11y_media_has_caption -->
    <video
      bind:this={video}
      controls
      playsinline
      preload="metadata"
      onloadedmetadata={onLoadedMetadata}
      oncanplay={() => {
        if (phase === 'starting') phase = 'ready';
      }}
      onerror={onVideoError}
    ></video>
    {#if phase === 'starting'}
      <div class="overlay passive"><span class="spinner"></span> Preparing stream…</div>
    {:else if phase === 'error'}
      <div class="overlay" role="alert">
        <strong>Cannot play this item</strong>
        <span>{error}</span>
        <button class="btn" onclick={start}>Retry</button>
      </div>
    {/if}
  </div>

  {#if playback}
    <details class="why">
      <summary>
        <span class="badge">{methodLabel(playback.method)}</span>
        <span class="muted">how this is being played</span>
      </summary>
      <p class="muted">
        video: {playback.videoAction}, audio: {playback.audioAction}
      </p>
      {#if playback.reasons.length}
        <ul>
          {#each playback.reasons as reason}<li>{reason}</li>{/each}
        </ul>
      {/if}
    </details>
  {/if}
</div>

<style>
  .frame {
    position: relative;
    background: #000;
    border-radius: var(--radius);
    overflow: hidden;
    aspect-ratio: 16 / 9;
    max-height: 80dvh;
  }
  video {
    width: 100%;
    height: 100%;
    display: block;
    background: #000;
  }
  .overlay {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.6rem;
    padding: 1rem;
    text-align: center;
    background: rgb(0 0 0 / 0.72);
  }
  .overlay.passive {
    flex-direction: row;
    pointer-events: none;
    color: var(--muted);
  }
  .overlay strong {
    color: var(--danger);
  }
  .spinner {
    width: 1.1em;
    height: 1.1em;
    border: 2px solid var(--line);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .why {
    margin-top: 0.8rem;
    font-size: 0.9rem;
  }
  .why summary {
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .why p,
  .why ul {
    margin: 0.4rem 0 0;
  }
  .why ul {
    padding-left: 1.2rem;
    color: var(--muted);
  }
</style>
