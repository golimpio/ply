<script lang="ts" module>
  export interface Track {
    id: string | number;
    label: string;
  }

  /** How long the overlay stays after the last activity while playing. */
  export const IDLE_MS = 3000;
</script>

<script lang="ts">
  import { untrack, type Snippet } from 'svelte';
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
   *   Overlays the host puts after it cover the picture and the bottom bar; the
   *   top bar (title, close) stays above them.
   * - `offset` is added to video.currentTime for display (0 when stream time is
   *   file time). `duration` is the item's known length, used while the media
   *   element still reports NaN/Infinity.
   * - onSeek(absSec) returning true means "handled" (e.g. a new session), and
   *   the controls leave currentTime alone.
   * - `info` is drawn in the top bar, between the title and the close button. An open <details>
   *   in it is a popover: it keeps the overlay up, and Escape or a click on the picture closes it.
   * - Pointer moves stop at the controls (see the plain listeners below): a Svelte onpointermove,
   *   onpointerover or onpointerout inside them never fires.
   * - `year`, for a film, follows its title.
   */
  let {
    video,
    title,
    subtitle = '',
    year,
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
    info,
  }: {
    video: HTMLVideoElement;
    title: string;
    subtitle?: string;
    year?: number;
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
    info?: Snippet;
  } = $props();

  let paused = $state(true);
  let current = $state(0);
  let mediaDuration = $state(0);
  let buffered = $state<[number, number][]>([]);
  let volume = $state(1);
  let muted = $state(false);
  let waiting = $state(false);
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

  function sync() {
    const v = video;
    paused = v.paused;
    current = v.currentTime;
    mediaDuration = v.duration;
    volume = v.volume;
    muted = v.muted;
    const b = v.buffered;
    const same = b.length === buffered.length && buffered.every(([s, e], i) => s === offset + b.start(i) && e === offset + b.end(i));
    if (!same) buffered = Array.from({ length: b.length }, (_, i) => [offset + b.start(i), offset + b.end(i)]);
  }

  $effect(() => {
    const v = video;
    // The clock and the buffer move several times a second; while the overlay is hidden nothing
    // shows them, so they wait for it to come back (the effect below catches up).
    const tick = () => visible && sync();
    const onWaiting = () => (waiting = true);
    const onPlaying = () => (waiting = false);
    const onTarget = (e: Event) =>
      (airplay = (e as Event & { availability?: string }).availability === 'available');
    const ticks = ['timeupdate', 'progress'];
    const events = ['durationchange', 'play', 'pause', 'volumechange', 'loadedmetadata', 'seeked'];
    // The lock screen extrapolates the position itself: it is told only when that would go wrong.
    const turns = ['play', 'pause', 'seeked', 'ratechange', 'loadedmetadata'];
    for (const ev of ticks) v.addEventListener(ev, tick);
    for (const ev of events) v.addEventListener(ev, sync);
    for (const ev of turns) v.addEventListener(ev, positionState);
    v.addEventListener('waiting', onWaiting);
    v.addEventListener('playing', onPlaying);
    v.addEventListener('webkitplaybacktargetavailabilitychanged', onTarget);
    void offset; // a new offset re-syncs; sync's own reads (buffered) must not re-run this
    untrack(sync);
    return () => {
      for (const ev of ticks) v.removeEventListener(ev, tick);
      for (const ev of events) v.removeEventListener(ev, sync);
      for (const ev of turns) v.removeEventListener(ev, positionState);
      v.removeEventListener('waiting', onWaiting);
      v.removeEventListener('playing', onPlaying);
      v.removeEventListener('webkitplaybacktargetavailabilitychanged', onTarget);
    };
  });

  // Shown again: the clock and the bars catch up at once, not at the next timeupdate.
  $effect(() => {
    if (visible) untrack(sync);
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
      ['play', () => void video.play().catch(() => {})],
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

  function positionState() {
    if (!('mediaSession' in navigator) || !total) return;
    try {
      navigator.mediaSession.setPositionState({
        duration: total,
        position: Math.min(offset + video.currentTime, total),
        playbackRate: video.playbackRate || 1,
      });
    } catch {
      // invalid state while metadata loads
    }
  }

  // ---- actions --------------------------------------------------------------

  function seekTo(abs: number) {
    const t = Math.max(0, total ? Math.min(abs, total - 0.5) : abs);
    if (onSeek?.(t) === true) return;
    video.currentTime = Math.max(0, t - offset);
    current = video.currentTime; // the fill stays where it was let go, not where it was until seeked
  }

  function skip(by: number) {
    seekTo(offset + video.currentTime + by);
    poke();
  }

  function toggle() {
    // play() rejects when a pause comes first (a quick second press): nothing to report.
    if (video.paused) video.play().catch(() => {});
    else video.pause();
    poke();
  }

  function setVolume(v: number) {
    video.volume = Math.max(0, Math.min(1, v));
    video.muted = video.volume === 0;
  }

  async function toggleFullscreen() {
    const container = video.parentElement;
    try {
      if (document.fullscreenElement) {
        await document.exitFullscreen();
      } else if (container?.requestFullscreen) {
        await container.requestFullscreen();
      } else {
        // iPhone Safari: only the video element itself can go fullscreen.
        (video as HTMLVideoElement & { webkitEnterFullscreen?: () => void }).webkitEnterFullscreen?.();
      }
    } catch {
      // refused (no user gesture, or not allowed here): the button stays as it is
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
  //
  // Activity (the mouse moving, a touch, a key) shows the overlay; IDLE_MS after the last of it,
  // while playing, it hides, the cursor with it. It stays while paused, with a menu or the info
  // popover open, while scrubbing, with the mouse on a control or keyboard focus inside. Playback
  // comes first: a mousemove only notes the time (no timer per move, no layout), a single timer does
  // the rest, and once the overlay is hidden nothing runs until the next activity.

  let root = $state<HTMLDivElement>();
  let awake = $state(true);
  let overControl = $state(false);
  let keyboardFocus = $state(false);
  let popover = $state(false); // a <details> of `info` is open
  const visible = $derived(
    awake || paused || menu !== null || popover || scrub !== null || overControl || keyboardFocus,
  );

  let lastActive = 0;
  let idleTimer: ReturnType<typeof setTimeout> | undefined;

  function poke() {
    lastActive = performance.now();
    awake = true;
    idleTimer ??= setTimeout(idle, IDLE_MS);
  }

  function idle() {
    const left = lastActive + IDLE_MS - performance.now();
    if (left > 0) {
      idleTimer = setTimeout(idle, left);
      return;
    }
    idleTimer = undefined;
    // A focused menu item removed with its menu fires no focusout.
    if (keyboardFocus && !root?.contains(document.activeElement)) keyboardFocus = false;
    awake = false;
  }

  /**
   * Focus that came into the controls by keyboard (Tab), not a button the mouse pressed: decided
   * as it arrives, since a key pressed later makes a mouse-focused button :focus-visible too.
   */
  function onFocusIn(e: FocusEvent) {
    keyboardFocus = (e.target as Element).matches(':focus-visible');
    poke();
  }

  // Playback starting or resuming shows the overlay for a moment, as activity would.
  $effect(() => {
    if (!paused) untrack(poke);
  });
  $effect(() => () => clearTimeout(idleTimer));

  /** What the mouse can be on that keeps the overlay up: a control, not the bars' empty space. */
  const CONTROL = 'button, input, summary, details[open], .scrubber, .menu';

  // Mousemoves come by the dozen a second: plain listeners, as Svelte's handlers each set a timer,
  // and they go no further than the player (the app's delegated pointermove listener, there for
  // the search palette and the jump rail, would set one per move too). So a Svelte onpointermove
  // (or onpointerover/onpointerout) inside .controls never fires: add a plain listener here instead.
  // pointerover only comes when the pointer crosses into another element, not on every move.
  $effect(() => {
    const el = root;
    if (!el) return;
    const move = (e: PointerEvent) => {
      e.stopPropagation();
      if (e.pointerType === 'mouse') poke();
    };
    const over = (e: PointerEvent) => {
      e.stopPropagation();
      overControl = e.pointerType === 'mouse' && !!(e.target as Element).closest(CONTROL);
    };
    const out = (e: PointerEvent) => {
      e.stopPropagation();
      if (!el.contains(e.relatedTarget as Node | null)) overControl = false;
    };
    // A <details> opening or closing (toggle doesn't bubble: caught on the way down).
    const toggled = () => (popover = !!el.querySelector('details[open]'));
    el.addEventListener('pointermove', move);
    el.addEventListener('pointerover', over);
    el.addEventListener('pointerout', out);
    el.addEventListener('toggle', toggled, true);
    return () => {
      el.removeEventListener('pointermove', move);
      el.removeEventListener('pointerover', over);
      el.removeEventListener('pointerout', out);
      el.removeEventListener('toggle', toggled, true);
    };
  });

  /** Closes an open popover of `info`; whether there was one. */
  function closePopover(): boolean {
    const open = root?.querySelector<HTMLDetailsElement>('details[open]');
    if (open) open.open = false;
    return !!open;
  }

  // ---- pointer & keyboard -----------------------------------------------------

  let lastPointer = 'mouse';
  function onPointerDown(e: PointerEvent) {
    lastPointer = e.pointerType;
    // A touch on the picture is a tap that shows or hides the overlay (onSurface decides which).
    if (e.pointerType === 'mouse' || e.target !== e.currentTarget) poke();
  }

  let lastTap = 0;
  function onSurface(e: PointerEvent) {
    if (e.target !== e.currentTarget) return; // a control, not the picture
    if (menu) {
      menu = null;
      return;
    }
    if (closePopover()) return;
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
      awake = false;
    } else {
      poke();
    }
  }

  /**
   * When full screen was last left. Chrome keeps the Escape that leaves full screen to itself; a
   * browser that also hands it to the page, after the fullscreenchange, would have it close the
   * player too. An Escape this soon after leaving is taken as that one (e2e/player-ui.mjs plays it).
   */
  let leftFullscreenAt = -Infinity;

  /** Escape takes off one layer at a time: an open menu or popover, then full screen, then the player. */
  function escape() {
    if (menu) menu = null;
    else if (closePopover()) return;
    else if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else if (performance.now() - leftFullscreenAt > 500) onClose?.();
  }

  function onFullscreenChange() {
    fullscreen = !!document.fullscreenElement;
    if (!fullscreen) leftFullscreenAt = performance.now();
  }

  /** Keys toggling a state: a held key must not flip it back and forth (nor Escape close layer after layer). */
  const TOGGLES = new Set([' ', 'k', 'f', 'm', 'c', 'Escape']);

  let seekInput = $state<HTMLInputElement>();

  function onKey(e: KeyboardEvent) {
    if (e.metaKey || e.ctrlKey || e.altKey) return; // browser and system shortcuts (Find, address bar, ...)
    const t = e.target as HTMLElement;
    const range = t instanceof HTMLInputElement && t.type === 'range';
    if (!range && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    if (document.querySelector('dialog[open]')) return;
    poke();
    // Space and Enter activate the focused button, link or menu item.
    if ((e.key === ' ' || e.key === 'Enter') && t.closest('button, a, summary, [role="menuitemradio"]')) return;
    if (e.repeat && TOGGLES.has(e.key)) {
      e.preventDefault();
      return;
    }
    // The volume slider moves itself.
    if (range && t !== seekInput && /^(Arrow|Home|End|Page)/.test(e.key)) return;
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
      Escape: escape,
    };
    // The seek slider, focused: its arrows all seek (as a slider's do), Home and End go to the ends.
    if (t === seekInput) {
      keys.ArrowUp = () => skip(5);
      keys.ArrowDown = () => skip(-5);
      keys.Home = () => seekTo(0);
      keys.End = () => seekTo(total);
    }
    const fn = keys[e.key];
    if (!fn) return;
    e.preventDefault(); // the slider's own step too: the seek goes through seekTo
    fn();
  }

  // ---- the scrubber -------------------------------------------------------------
  //
  // A pointer seeks to the fraction of the bar under it, which is what the fill and the tooltip
  // show. The <input> stays for the keyboard and assistive technology only: a native range maps a
  // click with the thumb's width taken off both ends, so a click left of the middle landed early and
  // one right of it late (53 s off at 5% of a 2 h film on a 1000 px bar). Its rect is read on
  // pointerdown and when it resizes, never on a move.

  let scrubber = $state<HTMLDivElement>();
  let bar: { left: number; width: number } | undefined;
  let dragging: number | undefined; // the pointer that is scrubbing

  function measure() {
    const r = scrubber!.getBoundingClientRect();
    bar = { left: r.left, width: r.width };
  }

  $effect(() => {
    const el = scrubber;
    if (!el) return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    el.addEventListener('pointermove', scrubMove); // plain, as the root's (see there)
    return () => {
      ro.disconnect();
      el.removeEventListener('pointermove', scrubMove);
    };
  });

  /** The fraction of the bar at clientX, 0 to 1. */
  const fractionAt = (clientX: number) =>
    bar && bar.width > 0 ? Math.max(0, Math.min(1, (clientX - bar.left) / bar.width)) : 0;

  function scrubStart(e: PointerEvent) {
    if (e.button !== 0 || !total) return;
    measure();
    scrubber!.setPointerCapture(e.pointerId);
    dragging = e.pointerId;
    scrub = fractionAt(e.clientX) * total;
  }

  function scrubMove(e: PointerEvent) {
    if (!bar) return;
    const f = fractionAt(e.clientX);
    hover = { x: f * bar.width, t: f * total };
    if (dragging === e.pointerId) scrub = f * total;
  }

  function scrubEnd(e: PointerEvent) {
    if (dragging !== e.pointerId) return;
    dragging = undefined;
    const t = fractionAt(e.clientX) * total;
    scrub = null;
    seekTo(t);
  }

  function scrubCancel(e: PointerEvent) {
    if (dragging !== e.pointerId) return;
    dragging = undefined;
    scrub = null;
  }

  const fraction = (t: number) => (total ? Math.max(0, Math.min(1, t / total)) : 0);
</script>

<svelte:window onkeydown={onKey} />
<svelte:document onfullscreenchange={onFullscreenChange} />

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  bind:this={root}
  class="controls"
  class:hidden={!visible}
  onfocusin={onFocusIn}
  onfocusout={(e) => {
    if (!root?.contains(e.relatedTarget as Node | null)) keyboardFocus = false;
  }}
  onpointerdown={onPointerDown}
  onpointerup={onSurface}
  ondblclick={(e) => e.target === e.currentTarget && lastPointer === 'mouse' && toggleFullscreen()}
>
  <div class="top">
    <h1 class="titles">
      {#if subtitle}<span class="subtitle">{subtitle}</span>{/if}
      <span class="title">{title}{#if year}<span class="year">{` (${year})`}</span>{/if}</span>
    </h1>
    {@render info?.()}
    {#if onClose}
      <button class="ctl" onclick={onClose} aria-label="Close player"><Icon name="close" /></button>
    {/if}
  </div>

  {#if waiting}<div class="spinner" aria-label="Buffering" role="status"></div>{/if}

  <div class="bottom">
    <div
      class="scrubber"
      bind:this={scrubber}
      role="presentation"
      onpointerdown={scrubStart}
      onpointerup={scrubEnd}
      onpointercancel={scrubCancel}
      onpointerleave={() => (hover = null)}
    >
      <div class="rail">
        {#each buffered as [a, b], i (i)}
          <span class="buffered" style:transform="translateX({fraction(a) * 100}%) scaleX({fraction(b) - fraction(a)})"></span>
        {/each}
        <span class="played" style:transform="scaleX({fraction(position)})"></span>
      </div>
      <input
        bind:this={seekInput}
        type="range"
        min="0"
        max={total || 0}
        step="any"
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
        <span class="tip tabular" style:transform="translateX({hover.x}px) translateX(-50%)">{formatClock(hover.t)}</span>
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
    user-select: none;
    -webkit-user-select: none;
    touch-action: manipulation;
  }
  /* The bars fade, not the whole: the root makes no stacking context, so the top bar (and its
     close button) stays above the host's overlays, and the buffering spinner stays in view. */
  .top,
  .bottom {
    transition: opacity var(--motion-fade) var(--ease);
  }
  .controls.hidden .top,
  .controls.hidden .bottom {
    opacity: 0;
  }
  .controls.hidden {
    cursor: none;
  }
  .controls.hidden * {
    pointer-events: none;
  }
  /* Into the safe areas: an iPhone held sideways has its notch and corners on the left and right. */
  .top,
  .bottom {
    padding: 0.75rem max(var(--gutter-player), env(safe-area-inset-right)) 0.75rem
      max(var(--gutter-player), env(safe-area-inset-left));
  }
  .top {
    position: relative;
    z-index: var(--z-over);
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
  /* The page's heading, in the bar's own type (not the app's h1). */
  .titles {
    flex: 1;
    min-width: 0;
    font-family: inherit;
    font-size: var(--fs-base);
    font-weight: var(--fw-regular);
    line-height: inherit;
    letter-spacing: normal;
  }
  .title,
  .subtitle {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .title {
    font-weight: var(--fw-bold);
    font-size: var(--fs-lg);
  }
  .subtitle,
  .year {
    color: var(--text-dim);
    font-weight: var(--fw-regular);
  }
  .subtitle {
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
    cursor: pointer;
    touch-action: none; /* a drag along it scrubs, it doesn't scroll */
  }
  .rail {
    position: absolute;
    left: 0;
    right: 0;
    height: 4px;
    border-radius: var(--radius-xs);
    background: var(--fill-track);
    overflow: hidden;
    transition: transform var(--motion-hover);
  }
  .scrubber:hover .rail {
    transform: scaleY(1.5);
  }
  .buffered,
  .played {
    position: absolute;
    inset: 0;
    transform-origin: left;
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
    pointer-events: none; /* the pointer is the bar's (see scrubStart) */
  }
  .scrubber input:focus-visible + .tip,
  .scrubber:has(input:focus-visible) .rail {
    outline: 2px solid var(--text);
    outline-offset: 4px;
  }
  .tip {
    position: absolute;
    left: 0;
    bottom: 1.6rem;
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
    flex: none;
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
  /* The slider takes its room at once and fades in: only opacity is animated. */
  .volume input {
    width: 0;
    opacity: 0;
    transition: opacity var(--motion-toggle);
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
