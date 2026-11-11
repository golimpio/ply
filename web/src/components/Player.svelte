<script lang="ts">
  import { onMount } from 'svelte';
  import type HlsType from 'hls.js';
  import { api, mediaUrl, type ItemDetail, type Playback, type Subtitle } from '../lib/api';
  import { channelLabel, codecLabel, episodeLabel, languageLabel, methodLabel } from '../lib/format';
  import { Heartbeat } from '../lib/heartbeat';
  import { applyProgress } from '../lib/library.svelte';
  import { AUTO_RETRIES, limitOf, probeMedia, type PlayLimit as Limit } from '../lib/playlimit';
  import { browserProfile } from '../lib/profile';
  import { Progress } from '../lib/progress';
  import { goBack, href } from '../lib/router.svelte';
  import { MAX_SESSION_RESTARTS, sessionGone, sessionStopped } from '../lib/session';
  import { artSrc } from '../lib/ui-types';
  import PlayerControls, { type Track } from './PlayerControls.svelte';
  import PlayLimit from './PlayLimit.svelte';

  let { item, startSec = 0 }: { item: ItemDetail; startSec?: number } = $props();

  let video = $state<HTMLVideoElement>();
  /** 'stopped': someone stopped this play on purpose (410), from another device or the Activity page. */
  let phase = $state<'starting' | 'ready' | 'error' | 'limit' | 'stopped'>('starting');
  let error = $state('');
  /** The server is at its limit of plays or conversions: what it said. Kept while a retry is asked. */
  let limit = $state<Limit>();
  /** The cap panel counts down and tries again on its own (not for ever: see AUTO_RETRIES). */
  let limitAuto = $state(true);
  let limitRetries = 0;
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
  /** Whether the current attempt plays once loaded; a retry at the limit keeps it (a paused player stays paused). */
  let wantPlay = true;
  /** Whether the stream torn down by pagehide was playing: a restore from the back/forward cache keeps that. */
  let leftPlaying = true;
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
  /**
   * Keeps the HLS session alive, paused or not (lib/heartbeat.ts); a direct play has none. A 404
   * means the server lost it (sessionLost); a 410 that someone stopped it (stoppedElsewhere).
   */
  const heartbeat = new Heartbeat({
    send: (sid, pos, paused) => api.heartbeat(sid, pos, paused),
    position: () => progress.position(),
    paused: () => el?.paused ?? true,
    onGone: (sid, pos) => {
      if (destroyed || sid !== sessionId) return;
      if (!sessionLost(attempt, pos, wantsToPlay())) fail('The server ended this stream.');
    },
    onStopped: (sid) => {
      if (!destroyed && sid === sessionId) stoppedElsewhere();
    },
  });
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

  /**
   * Whether the viewer wants this to play. Once a stream is ready that is the element's own state;
   * before (preparing, or at the limit) the element sits paused by release(), so it is what the last
   * start() was asked for.
   */
  function wantsToPlay(): boolean {
    return phase === 'ready' ? !(el?.paused ?? true) : wantPlay;
  }

  /** Start a new playback at the current position (a new audio track or burned-in subtitle), paused if it was. */
  function restart() {
    void start(progress.position(), wantsToPlay());
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
    heartbeat.beat();
  }

  function onPause() {
    stopTicker();
    progress.report();
    heartbeat.beat();
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
    heartbeat.stop();
    if (sessionId) {
      void api.deleteSession(sessionId);
      sessionId = undefined;
    }
  }

  function fail(message: string) {
    phase = 'error';
    error = message;
    limit = undefined;
    progress.report(); // where it got to, while the element still knows
    release();
  }

  /**
   * The server lost the session of attempt `run` (404: it restarted, or reaped it): carry on where it
   * was in a new one, playing or paused as it was. However many ways the loss is reported (a beat, a
   * segment, the element), it counts once: the first starts a new attempt, the others are stale.
   * False when it has been tried too often in a row (the caller decides how to fail).
   */
  function sessionLost(run: number, at: number, play: boolean): boolean {
    if (run !== attempt) return true;
    if (sessionRestarts >= MAX_SESSION_RESTARTS) return false;
    sessionRestarts++;
    void start(at, play);
    return true;
  }

  /** Someone stopped this play on purpose (410): say so, and start nothing until the viewer asks. */
  function stoppedElsewhere() {
    attempt++; // whatever is still on its way (a start, a probe) is for a play that is over
    progress.report();
    sessionId = undefined; // already ended by the server: nothing to delete
    release();
    limit = undefined;
    phase = 'stopped';
  }

  let frame = $state<HTMLDivElement>();

  /**
   * A retry got in: the cap panel goes. If it had the focus (or the page has it), the focus goes to
   * the player's play button rather than falling to the page.
   */
  function closeLimit() {
    if (!limit) return;
    const a = document.activeElement;
    const inPanel = a === document.body || !!frame?.querySelector('.overlay.limit')?.contains(a);
    limit = undefined;
    if (inPanel) frame?.querySelector<HTMLElement>('.controls .ctl.big')?.focus();
  }

  /** The server is at a limit: show what is playing instead of a broken video. */
  function refused(l: Limit) {
    progress.report();
    release();
    limit = l;
    limitAuto = limitRetries < AUTO_RETRIES;
    phase = 'limit';
  }

  /** From the cap panel: its countdown ran out (auto), or the viewer asked (or stopped a play). */
  function retryLimit(auto: boolean) {
    limitRetries = auto ? limitRetries + 1 : 0;
    void start(progress.position(), wantPlay);
  }

  /** Start a stream at `at` seconds into the file, replacing the current one; `play` it once loaded. */
  async function start(at: number, play = true) {
    const run = ++attempt;
    wantPlay = play;
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
      if (destroyed || run !== attempt) return;
      const l = limitOf(e);
      if (l) refused(l);
      else fail(e instanceof Error ? e.message : String(e));
      return;
    }
    if (destroyed || run !== attempt) {
      if (pb.sessionId) void api.deleteSession(pb.sessionId);
      return;
    }
    sessionId = pb.sessionId;
    playback = pb;
    closeLimit();
    if (pb.sessionId) heartbeat.start(pb.sessionId);

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
        if (sessionStopped(data)) {
          stoppedElsewhere(); // stopped on purpose: not to be started again on its own
        } else if (sessionGone(data)) {
          // The server ends a session that is not asked for anything for a while: the same URL will
          // never work again (hls.js would retry it for half a minute). Start a new session at the
          // current position instead; if that keeps failing the same way, stop (startLoad cannot
          // revive a playlist that 404s).
          if (!sessionLost(run, progress.position(), wantsToPlay()) && data.fatal) fail(`Stream error: ${data.details}`);
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
    if (play)
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
    const src = el.currentSrc;
    const playing = wantsToPlay();
    const why =
      code === MediaError.MEDIA_ERR_NETWORK
        ? 'a network error interrupted the stream'
        : code === MediaError.MEDIA_ERR_DECODE
          ? 'the media could not be decoded'
          : code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED
            ? 'this format is not supported'
            : 'playback failed';
    const message = `The browser could not play this item: ${why}.`;
    // A file that will not decode was served: there is no refusal to look for, and asking for the
    // file again would keep a direct play counted by the server for nothing.
    if (code === MediaError.MEDIA_ERR_DECODE) {
      fail(message);
      return;
    }
    // The element only says that it failed. Ask the URL (a direct play's file, or Safari's native HLS
    // playlist) why: stopped on purpose (410), at a limit (503), or for a playlist a lost session.
    const run = attempt;
    void probeMedia(src).then((r) => {
      if (destroyed || run !== attempt) return;
      if (r?.kind === 'stopped') stoppedElsewhere();
      else if (r?.kind === 'limit') refused(r.limit);
      else if (code === MediaError.MEDIA_ERR_NETWORK && isHlsUrl(src) && sessionLost(run, progress.position(), playing)) return;
      else fail(message);
    });
  }

  /** Leaving the page: report first (the media element is about to be torn down), then release. */
  function leave() {
    leftPlaying = wantsToPlay();
    progress.report(true);
    release();
  }

  /**
   * Back from the back/forward cache: pagehide tore the stream down (leave), so start a new one where
   * it was. A first load also fires pageshow, but not with `persisted`.
   */
  function restore(e: PageTransitionEvent) {
    if (e.persisted && !destroyed) void start(progress.position(), leftPlaying);
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
    const v = video!;
    el = v;
    // A plain listener: it fires several times a second, and Svelte's handlers each set a timer.
    const onTimeUpdate = () => progress.update();
    v.addEventListener('timeupdate', onTimeUpdate);
    subKey = defaultSubtitle(item.subtitles);
    void start(startSec);
    return () => {
      destroyed = true;
      leave();
      v.removeEventListener('timeupdate', onTimeUpdate);
    };
  });

  const title = $derived(item.kind === 'episode' ? item.episodeTitle || item.title : item.title);
  const subtitle = $derived(
    item.kind === 'episode' ? [item.show, episodeLabel(item.season, item.episode)].filter(Boolean).join(' · ') : '',
  );
</script>

<svelte:window onpagehide={leave} onpageshow={restore} />
<!-- iOS Safari often skips pagehide when the app is switched away from: report when the page is hidden. -->
<!-- Back in view: beat at once, so that a session lost while hidden (timers are throttled) is replaced now. -->
<svelte:document onvisibilitychange={() => (document.hidden ? progress.report(true) : heartbeat.beat())} />

<!-- How this is being played, in the top bar: the badge, and why on demand. -->
{#snippet how()}
  {#if playback}
    <details class="why">
      <summary title="Playback details" aria-label="Playback details">
        <span class="badge">{methodLabel(playback.method)}</span>
      </summary>
      <div class="why-body">
        <p class="muted">
          video: {playback.videoAction}, audio: {playback.audioAction}
        </p>
        {#if playback.reasons.length}
          <ul>
            {#each playback.reasons as reason}<li>{reason}</li>{/each}
          </ul>
        {/if}
      </div>
    </details>
  {/if}
{/snippet}

<div class="player">
  <div class="frame" bind:this={frame}>
    <!-- svelte-ignore a11y_media_has_caption -->
    <video
      bind:this={video}
      playsinline
      preload="metadata"
      onloadedmetadata={onLoadedMetadata}
      onplay={onPlay}
      onpause={onPause}
      onended={onEnded}
      oncanplay={() => {
        sessionRestarts = 0;
        limitRetries = 0;
        if (phase === 'starting') phase = 'ready';
      }}
      onerror={onVideoError}
    ></video>
    {#if video}
      <PlayerControls
        {video}
        {title}
        {subtitle}
        year={item.kind === 'episode' ? undefined : item.year || undefined}
        artwork={artSrc(item, 'backdrop')}
        duration={item.durationSec}
        {audioTracks}
        selectedAudio={audioIndex ?? playback?.audioIndex}
        subtitles={subtitleTracks}
        selectedSubtitle={subKey}
        onSelectAudio={selectAudio}
        onSelectSubtitle={selectSubtitle}
        onClose={() => goBack()}
        info={how}
      />
    {/if}
    {#if limit && (phase === 'limit' || phase === 'starting')}
      <div class="overlay limit">
        <PlayLimit {limit} auto={limitAuto} busy={phase === 'starting'} onRetry={retryLimit} />
      </div>
    {:else if phase === 'stopped'}
      <div class="overlay stopped" role="alert">
        <strong>Stopped from another device</strong>
        <span>Someone stopped this video from another device. It won't start again on its own.</span>
        <button class="btn primary" onclick={() => void start(progress.position())}>Play again</button>
      </div>
    {:else if phase === 'starting'}
      <div class="overlay passive"><span class="spinner"></span> Preparing stream…</div>
    {:else if phase === 'error'}
      <div class="overlay" role="alert">
        <strong>Cannot play this item</strong>
        <span>{error}</span>
        <button class="btn" onclick={retry}>Retry</button>
      </div>
    {/if}
  </div>
</div>

<style>
  /* The whole of the host (Play: the window); the picture letterboxed on black. */
  .player,
  .frame {
    position: absolute;
    inset: 0;
  }
  .frame {
    background: var(--surface-video);
    overflow: hidden;
  }
  video {
    width: 100%;
    height: 100%;
    display: block;
    background: var(--surface-video);
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
    background: var(--scrim-player);
  }
  .overlay.passive {
    flex-direction: row;
    pointer-events: none;
    color: var(--text-muted);
  }
  .overlay strong {
    color: var(--danger);
  }
  .overlay.stopped strong {
    color: var(--text);
    font-size: var(--fs-xl);
  }
  .overlay.limit {
    justify-content: safe center;
    overflow-y: auto;
    background: var(--scrim-player-strong);
  }
  .spinner {
    width: 1.1em;
    height: 1.1em;
    border: 2px solid var(--line);
    border-top-color: var(--accent);
    border-radius: var(--radius-round);
    animation: spin var(--motion-spin) var(--ease-linear) infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .why {
    flex: none;
    font-size: var(--fs-sm-3);
  }
  .why summary {
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .why-body {
    position: absolute;
    top: 100%;
    right: max(var(--gutter-player), env(safe-area-inset-right));
    max-width: min(26rem, calc(100vw - 2rem));
    padding: 0.6rem 0.8rem;
    background: var(--surface-popover);
    backdrop-filter: var(--blur-popover);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius);
    box-shadow: var(--shadow-popover);
  }
  .why p,
  .why ul {
    margin: 0;
  }
  .why ul {
    margin-top: 0.4rem;
    padding-left: 1.2rem;
    color: var(--text-muted);
  }

</style>
