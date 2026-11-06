<script lang="ts" module>
  export interface Track {
    id: string | number;
    label: string;
  }
</script>

<script lang="ts">
  import { formatClock } from '../lib/format';
  import Icon from './Icon.svelte';

  /*
   * Player chrome over a <video> owned by Player.svelte. It never loads media or
   * talks to the server: track changes and seeks the caller wants to handle go
   * out through callbacks.
   *
   * Contract for the host:
   * - Render this inside the same positioned container as the <video> (that
   *   container is what goes fullscreen) and drop the video's `controls` attribute.
   * - `offset` is added to video.currentTime for display (0 when stream time is
   *   file time). `duration` is the item's known length, used while the media
   *   element still reports NaN/Infinity.
   * - onSeek(absSec) returning true means "handled" (e.g. a new session), and
   *   the controls leave currentTime alone.
   */
  let {
    video,
    title,
    subtitle = '',
    artwork = '',
    offset = 0,
    duration = 0,
    audioTracks = [],
    selectedAudio,
    subtitles = [],
    selectedSubtitle = null,
    onSelectAudio,
    onSelectSubtitle,
    onSeek,
    onClose,
  }: {
    video: HTMLVideoElement;
    title: string;
    subtitle?: string;
    artwork?: string;
    offset?: number;
    duration?: number;
    audioTracks?: Track[];
    selectedAudio?: Track['id'];
    subtitles?: Track[];
    selectedSubtitle?: Track['id'] | null;
    onSelectAudio?: (id: Track['id']) => void;
    onSelectSubtitle?: (id: Track['id'] | null) => void;
    onSeek?: (absSec: number) => boolean | void;
    onClose?: () => void;
  } = $props();

  let paused = $state(true);
  let current = $state(0);
  let mediaDuration = $state(0);
  let buffered = $state<[number, number][]>([]);
  let volume = $state(1);
  let muted = $state(false);
  let waiting = $state(false);
  let visible = $state(true);
  let menu = $state<'audio' | 'subs' | null>(null);
  let fullscreen = $state(false);
  let airplay = $state(false);
  let scrub = $state<number | null>(null); // value while dragging the scrubber
  let hover = $state<{ x: number; t: number } | null>(null);

  const total = $derived(Number.isFinite(mediaDuration) && mediaDuration > 0 ? offset + mediaDuration : duration);
  const position = $derived(scrub ?? offset + current);
  const pipSupported =
    typeof document !== 'undefined' &&
    ((document as Document & { pictureInPictureEnabled?: boolean }).pictureInPictureEnabled ||
      'webkitSetPresentationMode' in HTMLVideoElement.prototype);

  // ---- media element → state ------------------------------------------------

  $effect(() => {
    const v = video;
    const sync = () => {
      paused = v.paused;
      current = v.currentTime;
      mediaDuration = v.duration;
      volume = v.volume;
      muted = v.muted;
      buffered = Array.from({ length: v.buffered.length }, (_, i) => [
        offset + v.buffered.start(i),
        offset + v.buffered.end(i),
      ]);
    };
    const onWaiting = () => (waiting = true);
    const onPlaying = () => (waiting = false);
    const onTarget = (e: Event) =>
      (airplay = (e as Event & { availability?: string }).availability === 'available');
    const events = ['timeupdate', 'durationchange', 'progress', 'play', 'pause', 'volumechange', 'loadedmetadata', 'seeked'];
    for (const ev of events) v.addEventListener(ev, sync);
    v.addEventListener('waiting', onWaiting);
    v.addEventListener('playing', onPlaying);
    v.addEventListener('webkitplaybacktargetavailabilitychanged', onTarget);
    sync();
    return () => {
      for (const ev of events) v.removeEventListener(ev, sync);
      v.removeEventListener('waiting', onWaiting);
      v.removeEventListener('playing', onPlaying);
      v.removeEventListener('webkitplaybacktargetavailabilitychanged', onTarget);
    };
  });

  // ---- Media Session: lock screen, headphones, keyboard media keys -----------

  $effect(() => {
    if (!('mediaSession' in navigator)) return;
    const ms = navigator.mediaSession;
    ms.metadata = new MediaMetadata({
      title,
      artist: subtitle,
      album: 'ply',
      artwork: artwork ? [{ src: artwork, sizes: '1600x900', type: 'image/jpeg' }] : [],
    });
    const handlers: [MediaSessionAction, MediaSessionActionHandler][] = [
      ['play', () => void video.play()],
      ['pause', () => video.pause()],
      ['seekbackward', (d) => skip(-(d.seekOffset ?? 10))],
      ['seekforward', (d) => skip(d.seekOffset ?? 10)],
      ['seekto', (d) => d.seekTime !== undefined && seekTo(d.seekTime)],
    ];
    for (const [action, fn] of handlers) {
      try {
        ms.setActionHandler(action, fn);
      } catch {
        // action not supported by this browser
      }
    }
    return () => {
      for (const [action] of handlers) {
        try {
          ms.setActionHandler(action, null);
        } catch {
          // ignore
        }
      }
      ms.metadata = null;
    };
  });

  $effect(() => {
    if (!('mediaSession' in navigator) || !total) return;
    try {
      navigator.mediaSession.setPositionState({
        duration: total,
        position: Math.min(offset + current, total),
        playbackRate: video.playbackRate || 1,
      });
    } catch {
      // invalid state while metadata loads
    }
  });

  // ---- actions --------------------------------------------------------------

  function seekTo(abs: number) {
    const t = Math.max(0, total ? Math.min(abs, total - 0.5) : abs);
    if (onSeek?.(t) === true) return;
    video.currentTime = Math.max(0, t - offset);
  }

  function skip(by: number) {
    seekTo(offset + video.currentTime + by);
    poke();
  }

  function toggle() {
    if (video.paused) void video.play();
    else video.pause();
    poke();
  }

  function setVolume(v: number) {
    video.volume = Math.max(0, Math.min(1, v));
    video.muted = video.volume === 0;
  }

  async function toggleFullscreen() {
    const container = video.parentElement;
    if (document.fullscreenElement) {
      await document.exitFullscreen();
    } else if (container?.requestFullscreen) {
      await container.requestFullscreen();
    } else {
      // iPhone Safari: only the video element itself can go fullscreen.
      (video as HTMLVideoElement & { webkitEnterFullscreen?: () => void }).webkitEnterFullscreen?.();
    }
  }

  async function togglePip() {
    const v = video as HTMLVideoElement & { webkitSetPresentationMode?: (m: string) => void };
    if (document.pictureInPictureElement) await document.exitPictureInPicture();
    else if (v.requestPictureInPicture && document.pictureInPictureEnabled) await v.requestPictureInPicture();
    else v.webkitSetPresentationMode?.('picture-in-picture');
  }

  function showAirPlay() {
    (video as HTMLVideoElement & { webkitShowPlaybackTargetPicker?: () => void }).webkitShowPlaybackTargetPicker?.();
  }

  function cycleSubtitles() {
    if (!subtitles.length || !onSelectSubtitle) return;
    const i = subtitles.findIndex((s) => s.id === selectedSubtitle);
    onSelectSubtitle(i + 1 < subtitles.length ? subtitles[i + 1].id : null);
  }

  // ---- auto-hide --------------------------------------------------------------

  let root = $state<HTMLDivElement>();
  let hideTimer: ReturnType<typeof setTimeout> | undefined;
  function poke() {
    visible = true;
    clearTimeout(hideTimer);
    hideTimer = setTimeout(() => {
      // Never hide controls a keyboard user is working in (focus must stay visible).
      const keyboardFocus = root?.querySelector(':focus-visible');
      if (!video.paused && !menu && scrub === null && !keyboardFocus) visible = false;
    }, 2600);
  }
  $effect(() => {
    if (paused) {
      visible = true;
      clearTimeout(hideTimer);
    } else poke();
    return () => clearTimeout(hideTimer);
  });

  // ---- pointer & keyboard -----------------------------------------------------

  let lastTap = 0;
  function onSurface(e: PointerEvent) {
    if (e.target !== e.currentTarget) return; // a control, not the picture
    if (menu) {
      menu = null;
      return;
    }
    if (e.pointerType === 'mouse') {
      toggle();
      return;
    }
    // Touch: tap shows or hides the controls; double-tap on a side skips 10 s.
    const now = performance.now();
    if (now - lastTap < 300) {
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
      skip(e.clientX < rect.left + rect.width / 2 ? -10 : 10);
      lastTap = 0;
      return;
    }
    lastTap = now;
    if (visible && !paused) {
      visible = false;
    } else {
      poke();
    }
  }

  /** Keys toggling a state: a held key must not flip it back and forth. */
  const TOGGLES = new Set([' ', 'k', 'f', 'm', 'c']);

  function onKey(e: KeyboardEvent) {
    if (e.metaKey || e.ctrlKey || e.altKey) return; // browser and system shortcuts (Find, address bar, ...)
    const t = e.target as HTMLElement;
    const scrubber = t instanceof HTMLInputElement && t.type === 'range';
    if (!scrubber && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    if (document.querySelector('dialog[open]')) return;
    // Space and Enter activate the focused button, link or menu item.
    if ((e.key === ' ' || e.key === 'Enter') && t.closest('button, a, summary, [role="menuitemradio"]')) return;
    if (e.repeat && TOGGLES.has(e.key)) {
      e.preventDefault();
      return;
    }
    const keys: Record<string, () => void> = {
      ' ': toggle,
      k: toggle,
      j: () => skip(-10),
      l: () => skip(10),
      ArrowLeft: () => skip(-5),
      ArrowRight: () => skip(5),
      ArrowUp: () => setVolume(video.volume + 0.1),
      ArrowDown: () => setVolume(video.volume - 0.1),
      f: () => void toggleFullscreen(),
      m: () => (video.muted = !video.muted),
      c: cycleSubtitles,
      Escape: () => (menu = null),
    };
    const fn = keys[e.key];
    if (!fn) return;
    // Let the focused scrubber handle its own arrow keys.
    if (t instanceof HTMLInputElement && t.type === 'range' && e.key.startsWith('Arrow')) return;
    e.preventDefault();
    fn();
    poke();
  }

  function onTrackHover(e: PointerEvent) {
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const x = Math.max(0, Math.min(e.clientX - rect.left, rect.width));
    hover = { x, t: total ? (x / rect.width) * total : 0 };
  }

  const pct = (t: number) => (total ? `${Math.max(0, Math.min(100, (t / total) * 100))}%` : '0%');
</script>

<svelte:window onkeydown={onKey} />
<svelte:document onfullscreenchange={() => (fullscreen = !!document.fullscreenElement)} />

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  bind:this={root}
  class="controls"
  class:hidden={!visible}
  onfocusin={poke}
  onpointermove={(e) => e.pointerType === 'mouse' && poke()}
  onpointerup={onSurface}
  ondblclick={(e) => e.target === e.currentTarget && toggleFullscreen()}
>
  <div class="top">
    {#if onClose}
      <button class="ctl" onclick={onClose} aria-label="Close player"><Icon name="left" /></button>
    {/if}
    <div class="titles">
      <div class="title">{title}</div>
      {#if subtitle}<div class="subtitle">{subtitle}</div>{/if}
    </div>
  </div>

  {#if waiting}<div class="spinner" aria-label="Buffering" role="status"></div>{/if}

  <div class="bottom">
    <div class="scrubber" onpointermove={onTrackHover} onpointerleave={() => (hover = null)} role="presentation">
      <div class="rail">
        {#each buffered as [a, b], i (i)}
          <span class="buffered" style:left={pct(a)} style:width={`calc(${pct(b)} - ${pct(a)})`}></span>
        {/each}
        <span class="played" style:width={pct(position)}></span>
      </div>
      <input
        type="range"
        min="0"
        max={total || 0}
        step="1"
        value={position}
        aria-label="Seek"
        aria-valuetext="{formatClock(position)} of {formatClock(total)}"
        oninput={(e) => (scrub = Number(e.currentTarget.value))}
        onchange={(e) => {
          scrub = null;
          seekTo(Number(e.currentTarget.value));
        }}
      />
      {#if hover && total}
        <span class="tip tabular" style:left="{hover.x}px">{formatClock(hover.t)}</span>
      {/if}
    </div>

    <div class="row">
      <button class="ctl big" onclick={toggle} aria-label={paused ? 'Play' : 'Pause'}>
        <Icon name={paused ? 'play' : 'pause'} />
      </button>
      <button class="ctl skip" onclick={() => skip(-10)} aria-label="Back 10 seconds"><Icon name="back10" /></button>
      <button class="ctl skip" onclick={() => skip(10)} aria-label="Forward 10 seconds"><Icon name="fwd10" /></button>
      <div class="volume">
        <button class="ctl" onclick={() => (video.muted = !video.muted)} aria-label={muted ? 'Unmute' : 'Mute'}>
          <Icon name={muted || volume === 0 ? 'mute' : 'volume'} />
        </button>
        <input
          type="range"
          min="0"
          max="1"
          step="0.05"
          value={muted ? 0 : volume}
          aria-label="Volume"
          oninput={(e) => setVolume(Number(e.currentTarget.value))}
        />
      </div>
      <span class="time tabular">{formatClock(position)} <span class="muted">/ {formatClock(total)}</span></span>

      <div class="grow"></div>

      {#if subtitles.length && onSelectSubtitle}
        <div class="menu-anchor">
          <button
            class="ctl"
            class:on={selectedSubtitle !== null}
            onclick={() => (menu = menu === 'subs' ? null : 'subs')}
            aria-label="Subtitles"
            aria-expanded={menu === 'subs'}
          >
            <Icon name="cc" />
          </button>
          {#if menu === 'subs'}
            <div class="menu" role="menu" aria-label="Subtitles">
              <button role="menuitemradio" aria-checked={selectedSubtitle === null} onclick={() => { onSelectSubtitle(null); menu = null; }}>Off</button>
              {#each subtitles as s (s.id)}
                <button role="menuitemradio" aria-checked={selectedSubtitle === s.id} onclick={() => { onSelectSubtitle(s.id); menu = null; }}>
                  {s.label}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
      {#if audioTracks.length > 1 && onSelectAudio}
        <div class="menu-anchor">
          <button class="ctl" onclick={() => (menu = menu === 'audio' ? null : 'audio')} aria-label="Audio track" aria-expanded={menu === 'audio'}>
            <Icon name="audio" />
          </button>
          {#if menu === 'audio'}
            <div class="menu" role="menu" aria-label="Audio track">
              {#each audioTracks as a (a.id)}
                <button role="menuitemradio" aria-checked={selectedAudio === a.id} onclick={() => { onSelectAudio(a.id); menu = null; }}>
                  {a.label}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
      {#if airplay}
        <button class="ctl" onclick={showAirPlay} aria-label="AirPlay"><Icon name="airplay" /></button>
      {/if}
      {#if pipSupported}
        <button class="ctl pip" onclick={togglePip} aria-label="Picture in picture"><Icon name="pip" /></button>
      {/if}
      <button class="ctl" onclick={toggleFullscreen} aria-label={fullscreen ? 'Exit full screen' : 'Full screen'}>
        <Icon name={fullscreen ? 'exitfs' : 'fullscreen'} />
      </button>
    </div>
  </div>
</div>

<style>
  .controls {
    position: absolute;
    inset: 0;
    display: grid;
    grid-template-rows: auto 1fr auto;
    grid-template-columns: minmax(0, 1fr); /* never wider than the frame */
    color: var(--text);
    transition: opacity var(--motion-fade) var(--ease);
    user-select: none;
    -webkit-user-select: none;
    touch-action: manipulation;
  }
  .controls.hidden {
    opacity: 0;
    cursor: none;
  }
  .controls.hidden * {
    pointer-events: none;
  }
  /* A keyboard user tabbing into hidden controls sees them at once. */
  .controls.hidden:has(:focus-visible) {
    opacity: 1;
    cursor: auto;
  }
  .controls.hidden:has(:focus-visible) * {
    pointer-events: auto;
  }
  .top,
  .bottom {
    padding: 0.75rem clamp(0.75rem, 2.5vw, 1.5rem);
  }
  .top {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding-top: max(0.75rem, env(safe-area-inset-top));
    background: linear-gradient(to bottom, var(--scrim-player-top), transparent);
  }
  .bottom {
    grid-row: 3;
    padding-bottom: max(0.75rem, env(safe-area-inset-bottom));
    /* Not "to top, scrim, transparent": with a literal colour the minifier rewrote it into this
       top-to-bottom form, which Chrome rasterises a row differently; it leaves var() gradients
       alone, so this form keeps the pixels the tokens started from (see Hero.svelte's scrim). */
    background: linear-gradient(transparent, var(--scrim-player-bottom));
  }
  .titles {
    min-width: 0;
  }
  .title {
    font-weight: var(--fw-bold);
    font-size: var(--fs-lg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .subtitle {
    color: var(--text-dim);
    font-size: var(--fs-sm);
  }
  .spinner {
    grid-row: 2;
    place-self: center;
    width: 2.75rem;
    height: 2.75rem;
    border: 3px solid var(--line-spinner);
    border-top-color: var(--text);
    border-radius: var(--radius-round);
    animation: spin var(--motion-spin) var(--ease-linear) infinite;
    pointer-events: none;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .scrubber {
    position: relative;
    height: 1.25rem;
    display: flex;
    align-items: center;
  }
  .rail {
    position: absolute;
    left: 0;
    right: 0;
    height: 4px;
    border-radius: var(--radius-xs);
    background: var(--fill-track);
    overflow: hidden;
    transition: height var(--motion-hover);
  }
  .scrubber:hover .rail {
    height: 6px;
  }
  .buffered,
  .played {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 0;
  }
  .buffered {
    background: var(--fill-buffered);
  }
  .played {
    background: var(--tint);
  }
  .scrubber input {
    position: absolute;
    inset: 0;
    width: 100%;
    margin: 0;
    opacity: 0;
    cursor: pointer;
  }
  .scrubber input:focus-visible + .tip,
  .scrubber:has(input:focus-visible) .rail {
    outline: 2px solid var(--text);
    outline-offset: 4px;
  }
  .tip {
    position: absolute;
    bottom: 1.6rem;
    transform: translateX(-50%);
    padding: 0.15rem 0.45rem;
    font-size: var(--fs-xs);
    font-weight: var(--fw-semibold);
    background: var(--surface-tooltip);
    border-radius: var(--radius-sm);
    pointer-events: none;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    min-width: 0;
    align-items: center;
    gap: 0.25rem;
    margin-top: 0.35rem;
  }
  .grow {
    flex: 1;
  }
  .ctl {
    display: inline-grid;
    place-items: center;
    width: 2.75rem;
    height: 2.75rem;
    padding: 0;
    color: inherit;
    background: none;
    border: 0;
    border-radius: var(--radius-round);
    cursor: pointer;
  }
  .ctl:hover {
    background: var(--fill-strong);
  }
  .ctl :global(svg) {
    width: 1.5rem;
    height: 1.5rem;
  }
  .ctl.big :global(svg) {
    width: 1.9rem;
    height: 1.9rem;
  }
  .ctl.on {
    color: var(--tint);
  }
  .volume {
    display: flex;
    align-items: center;
  }
  .volume input {
    width: 0;
    opacity: 0;
    transition:
      width var(--motion-toggle),
      opacity var(--motion-toggle);
    accent-color: var(--text);
  }
  .volume:hover input,
  .volume:focus-within input {
    width: 5.5rem;
    opacity: 1;
  }
  .time {
    margin-left: 0.5rem;
    font-size: var(--fs-sm-3);
  }
  .menu-anchor {
    position: relative;
  }
  .menu {
    position: absolute;
    right: 0;
    bottom: 3.25rem;
    min-width: 12rem;
    max-height: 50vh;
    overflow-y: auto;
    padding: 0.35rem;
    background: var(--surface-popover);
    backdrop-filter: var(--blur-popover);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius);
    box-shadow: var(--shadow-popover);
  }
  .menu button {
    display: block;
    width: 100%;
    padding: 0.55rem 0.8rem 0.55rem 2rem;
    font: inherit;
    font-size: var(--fs-sm-3);
    text-align: left;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: var(--radius-item);
    cursor: pointer;
    position: relative;
  }
  .menu button:hover {
    background: var(--fill);
  }
  .menu button[aria-checked='true']::before {
    content: '';
    position: absolute;
    left: 0.8rem;
    top: 50%;
    width: 0.45rem;
    height: 0.45rem;
    margin-top: -0.225rem;
    border-radius: var(--radius-round);
    background: var(--tint);
  }
  @media (max-width: 600px) {
    /* Phones: double-tap skips 10 s and PiP is a system gesture; Full screen,
       subtitles and audio always stay. */
    .ctl {
      width: 2.5rem;
      height: 2.5rem;
    }
    .ctl.skip,
    .ctl.pip,
    .volume,
    .time .muted {
      display: none;
    }
  }
</style>
