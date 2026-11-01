<script lang="ts">
  import { onMount } from 'svelte';
  import type HlsType from 'hls.js';
  import { api, mediaUrl, type ItemDetail, type Playback, type Subtitle } from '../lib/api';
  import { channelLabel, codecLabel, episodeLabel, languageLabel, methodLabel } from '../lib/format';
  import { applyProgress } from '../lib/library.svelte';
  import { browserProfile } from '../lib/profile';
  import { Progress } from '../lib/progress';
  import { href } from '../lib/router.svelte';
  import { MAX_SESSION_RESTARTS, sessionGone } from '../lib/session';
  import { artSrc } from '../lib/ui-types';
  import PlayerControls, { type Track } from './PlayerControls.svelte';

  let { item, startSec = 0 }: { item: ItemDetail; startSec?: number } = $props();

  let video = $state<HTMLVideoElement>();
  let phase = $state<'starting' | 'ready' | 'error'>('starting');
  let error = $state('');
  let playback = $state<Playback>();
  /** The audio stream the viewer picked; undefined until they pick one (the server chooses). */
  let audioIndex = $state<number>();
  /** Key of the selected subtitle track (see Subtitle.key), null for none. */
  let subKey = $state<string | null>(null);

  // Non-reactive handles used for teardown and async-race checks.
  let el: HTMLVideoElement | undefined;
  let hls: HlsType | undefined;
  let sessionId: string | undefined;
  let destroyed = false;
  let attempt = 0;
  let resumed = false;
  /**
   * Resume state of the <video> across streams (lib/progress.ts): where each stream must start, what
   * position counts, where to start the next one from. Unloading the element makes its clock 0, so
   * nothing here may be read from the element except through this.
   */
  const progress = new Progress(
    () => el,
    (pos, dur, keepalive) => {
      api.putProgress(item.id, pos, dur, keepalive).then(
        (p) => applyProgress(item.id, p),
        () => {
          // best effort: the next report carries the position anyway
        },
      );
    },
    () => item.durationSec,
  );
  /** The hls.js chunk would not load: the browser remembers that for the life of the page. */
  let chunkFailed = false;
  /** Times in a row the server dropped our session and we started another (reset once playback runs). */
  let sessionRestarts = 0;

  const isHlsUrl = (url: string) => new URL(url).pathname.endsWith('.m3u8');

  // ---- tracks ------------------------------------------------------------------

  // A text track is shown by the browser (<track>); a bitmap track is burned into the video by the
  // server, which means a new session. Tracks of other kinds (no url, not bitmap) cannot be shown.
  const usableSubs = $derived(item.subtitles.filter((s) => s.image || s.url));
  const selectedSub = $derived(usableSubs.find((s) => s.key === subKey));
  const textSub = $derived(selectedSub && !selectedSub.image ? selectedSub : undefined);
  const burnIndex = $derived(selectedSub?.image ? Number(selectedSub.key) : undefined);

  const audioTracks = $derived<Track[]>(
    item.streams
      .filter((s) => s.type === 'audio')
      .map((s) => ({
        id: s.index,
        label:
          [languageLabel(s.language) || 'Unknown', s.title, codecLabel(s.codec), channelLabel(s.channels)]
            .filter(Boolean)
            .join(' · '),
      })),
  );
  const subtitleTracks = $derived<Track[]>(
    usableSubs.map((s) => ({
      id: s.key,
      label: [
        languageLabel(s.language) || 'Unknown',
        s.title,
        s.forced && 'forced',
        s.key.startsWith('ext') && 'external',
        s.image && 'burned in',
      ]
        .filter(Boolean)
        .join(' · '),
    })),
  );

  /**
   * The file's own choice: a track marked default, else a forced one. Only text tracks qualify: a
   * bitmap track would turn the whole playback into a transcode before the viewer asked for it.
   */
  function defaultSubtitle(subs: Subtitle[]): string | null {
    const text = subs.filter((s) => !s.image && s.url);
    return (text.find((s) => s.default) ?? text.find((s) => s.forced))?.key ?? null;
  }

  // Show the selected text track. The track element lives and dies with the selection; its cue times
  // are file times, which is what the video's clock is for direct play and HLS alike.
  $effect(() => {
    const v = video;
    const sub = textSub;
    if (!v || !sub?.url) return;
    const t = document.createElement('track');
    t.kind = 'subtitles';
    t.label = subtitleTracks.find((x) => x.id === sub.key)?.label ?? sub.key;
    if (sub.language) t.srclang = sub.language.slice(0, 2);
    t.src = mediaUrl(sub.url);
    v.appendChild(t);
    t.track.mode = 'showing';
    return () => t.remove();
  });

  function selectSubtitle(id: Track['id'] | null) {
    const wasBurning = burnIndex !== undefined;
    subKey = id === null ? null : String(id);
    if (wasBurning || burnIndex !== undefined) restart(); // a burned-in track is part of the stream
  }

  function selectAudio(id: Track['id']) {
    if (id === (audioIndex ?? playback?.audioIndex)) return;
    audioIndex = Number(id);
    restart();
  }

  /** Start a new playback at the current position (a new audio track or burned-in subtitle). */
  function restart() {
    void start(progress.position());
  }

  // ---- progress ------------------------------------------------------------------

  let ticker: ReturnType<typeof setInterval> | undefined;

  function startTicker() {
    clearInterval(ticker);
    ticker = setInterval(() => progress.report(), 10_000);
  }

  function stopTicker() {
    clearInterval(ticker);
    ticker = undefined;
  }

  function onPlay() {
    startTicker();
  }

  function onPause() {
    stopTicker();
    progress.report();
  }

  function onEnded() {
    stopTicker();
    progress.report();
  }

  // ---- playback ------------------------------------------------------------------

  /** Stop media, drop hls.js and end the server session (best effort, survives page unload). */
  function release() {
    // Unloading resets the clock to 0 and fires `timeupdate` and `pause` for it: keep the position
    // first, and nothing reports until the next stream is at its start.
    progress.end();
    stopTicker();
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
    progress.report(); // where it got to, while the element still knows
    release();
  }

  /** Start a stream at `at` seconds into the file, replacing the current one. */
  async function start(at: number) {
    const run = ++attempt;
    progress.report(); // the position of the stream we are about to replace (a no-op unless it counts)
    release();
    progress.begin(at);
    phase = 'starting';
    error = '';
    playback = undefined;
    resumed = false;

    let pb: Playback;
    try {
      pb = await api.playback(item.id, {
        custom: browserProfile(),
        startSec: at > 0 ? at : undefined,
        audioIndex,
        subtitleIndex: burnIndex,
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
      // Without a url the server could not stream it (streaming disabled); the last reason says why.
      const why = pb.reasons[pb.reasons.length - 1];
      fail(
        pb.method === 'direct'
          ? 'The server returned no stream URL for this item.'
          : `This file needs ${pb.method} to play in this browser, but the server could not start a stream${why ? ` (${why})` : ''}.`,
      );
      return;
    }

    const url = mediaUrl(pb.url);
    const v = el!;
    if (!isHlsUrl(url) || v.canPlayType('application/vnd.apple.mpegurl')) {
      v.src = url; // direct file, or native HLS (Safari / iOS)
    } else {
      let Hls: typeof HlsType;
      try {
        ({ default: Hls } = await import('hls.js'));
      } catch {
        // A lazy chunk that will not load (flaky network, or a server upgraded under an open tab).
        if (!destroyed && run === attempt) {
          chunkFailed = true;
          fail('Could not load the video player. Check the connection and retry.');
        }
        return;
      }
      if (destroyed || run !== attempt) return;
      if (!Hls.isSupported()) {
        fail('This browser cannot play HLS streams.');
        return;
      }
      // The playlist is the whole file, so a resume is just a start position: the server cuts in
      // at that segment, and later seeks anywhere are plain segment requests it answers by itself.
      const h = new Hls({
        startPosition: at > 0 ? at : -1,
        backBufferLength: 60,
        maxBufferLength: 40,
      });
      let networkRetries = 0;
      let mediaRetries = 0;
      h.on(Hls.Events.ERROR, (_evt, data) => {
        if (h !== hls) return;
        if (sessionGone(data)) {
          // The server ends a session that is not asked for anything for a while, and a paused player
          // with a full buffer asks for nothing: the same URL will never work again (hls.js would
          // retry it for half a minute). Start a new session at the current position instead; if
          // that keeps failing the same way, stop (startLoad cannot revive a playlist that 404s).
          if (sessionRestarts < MAX_SESSION_RESTARTS) {
            sessionRestarts++;
            restart();
          } else if (data.fatal) fail(`Stream error: ${data.details}`);
        } else if (!data.fatal) {
          return;
        } else if (data.type === Hls.ErrorTypes.NETWORK_ERROR && networkRetries++ < 3) h.startLoad();
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

  // Resume: stream time 0 is file time 0, so a resume is a plain seek once metadata is known. The
  // target is what we asked for, not what the server echoed (it did not, for direct play).
  function onLoadedMetadata() {
    const at = progress.target;
    if (!resumed && el && at > 0 && Math.abs(el.currentTime - at) > 1) el.currentTime = at;
    resumed = true;
  }

  function onVideoError() {
    if (!el?.currentSrc) return; // fired by our own teardown
    const code = el.error?.code;
    if (code === MediaError.MEDIA_ERR_NETWORK && isHlsUrl(el.currentSrc) && sessionRestarts < MAX_SESSION_RESTARTS) {
      sessionRestarts++; // native HLS (Safari): the same, a session that expired while paused
      restart();
      return;
    }
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

  /** Leaving the page: report first (the media element is about to be torn down), then release. */
  function leave() {
    progress.report(true);
    release();
  }

  /**
   * Back from the back/forward cache: pagehide tore the stream down (leave), so start a new one where
   * it was. A first load also fires pageshow, but not with `persisted`.
   */
  function restore(e: PageTransitionEvent) {
    if (e.persisted && !destroyed) void start(progress.position());
  }

  function retry() {
    if (chunkFailed) {
      // A failed dynamic import stays failed until the page is reloaded; carry the position over.
      history.replaceState(null, '', href.play(item.id, progress.position()));
      location.reload();
      return;
    }
    void start(progress.position());
  }

  onMount(() => {
    el = video;
    subKey = defaultSubtitle(item.subtitles);
    void start(startSec);
    return () => {
      destroyed = true;
      leave();
    };
  });

  const title = $derived(item.kind === 'episode' ? item.episodeTitle || item.title : item.title);
  const subtitle = $derived(
    item.kind === 'episode' ? [item.show, episodeLabel(item.season, item.episode)].filter(Boolean).join(' · ') : '',
  );
</script>

<svelte:window onpagehide={leave} onpageshow={restore} />
<!-- iOS Safari often skips pagehide when the app is switched away from: report when the page is hidden. -->
<svelte:document onvisibilitychange={() => document.hidden && progress.report(true)} />

<div class="player">
  <div class="frame">
    <!-- svelte-ignore a11y_media_has_caption -->
    <video
      bind:this={video}
      playsinline
      preload="metadata"
      onloadedmetadata={onLoadedMetadata}
      ontimeupdate={() => progress.update()}
      onplay={onPlay}
      onpause={onPause}
      onended={onEnded}
      oncanplay={() => {
        sessionRestarts = 0;
        if (phase === 'starting') phase = 'ready';
      }}
      onerror={onVideoError}
    ></video>
    {#if video}
      <PlayerControls
        {video}
        {title}
        {subtitle}
        artwork={artSrc(item, 'backdrop')}
        duration={item.durationSec}
        {audioTracks}
        selectedAudio={audioIndex ?? playback?.audioIndex}
        subtitles={subtitleTracks}
        selectedSubtitle={subKey}
        onSelectAudio={selectAudio}
        onSelectSubtitle={selectSubtitle}
        onClose={() => (location.hash = href.item(item.id))}
      />
    {/if}
    {#if phase === 'starting'}
      <div class="overlay passive"><span class="spinner"></span> Preparing stream…</div>
    {:else if phase === 'error'}
      <div class="overlay" role="alert">
        <strong>Cannot play this item</strong>
        <span>{error}</span>
        <button class="btn" onclick={retry}>Retry</button>
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
  .frame:fullscreen {
    aspect-ratio: auto;
    max-height: none;
    border-radius: 0;
  }
  video {
    width: 100%;
    height: 100%;
    display: block;
    background: #000;
    object-fit: contain;
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
