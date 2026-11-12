import type { Receiver } from './api';

export interface AirPlayVideo extends HTMLVideoElement {
  webkitShowPlaybackTargetPicker?: () => void;
  webkitCurrentPlaybackTargetIsWireless?: boolean;
}

export interface AirPlayState {
  supported: boolean;
  available: boolean;
  wireless: boolean;
}

/** Native URLs are transferable to the TV; a MediaSource blob is not. */
export function supportsAirPlay(video: AirPlayVideo): boolean {
  return typeof video.webkitShowPlaybackTargetPicker === 'function' &&
    video.canPlayType('application/vnd.apple.mpegurl') !== '';
}

export function observeAirPlay(video: AirPlayVideo, update: (state: AirPlayState) => void): () => void {
  const supported = supportsAirPlay(video);
  let available = false;
  const sync = () => update({
    supported,
    available: supported && (available || video.webkitCurrentPlaybackTargetIsWireless === true),
    wireless: supported && video.webkitCurrentPlaybackTargetIsWireless === true,
  });
  const target = (event: Event) => {
    available = (event as Event & { availability?: string }).availability === 'available';
    sync();
  };
  sync();
  if (!supported) return () => {};
  video.addEventListener('webkitplaybacktargetavailabilitychanged', target);
  video.addEventListener('webkitcurrentplaybacktargetiswirelesschanged', sync);
  return () => {
    video.removeEventListener('webkitplaybacktargetavailabilitychanged', target);
    video.removeEventListener('webkitcurrentplaybacktargetiswirelesschanged', sync);
  };
}

/** Call directly from the click handler: Safari requires the user's activation. */
export function showAirPlay(video: AirPlayVideo): boolean {
  if (!supportsAirPlay(video)) return false;
  try {
    video.webkitShowPlaybackTargetPicker!();
    return true;
  } catch {
    return false;
  }
}

/** The base an absolute media URL names ("http://192.168.4.196:8420"); "" for a relative one. */
export function playBase(url: string): string {
  if (!/^https?:\/\//.test(url)) return '';
  try {
    return new URL(url).origin;
  } catch {
    return '';
  }
}

/**
 * Whether a play's URL already sends a target at `base` somewhere it can fetch from: the play is on
 * that target's network, or it is relative to a page that is (a relative URL is the page's address).
 * The server routes an AirPlay play this way when the TVs on the LAN share one network.
 */
export function reachesTarget(url: string, base: string | undefined, pageHost: string): boolean {
  if (!base) return false;
  const mine = playBase(url);
  if (mine) return mine === base;
  try {
    return new URL(base).host === pageHost;
  } catch {
    return false;
  }
}

/**
 * Whether the AirPlay button must offer the TVs by name before Safari's picker. It does only when the
 * choice changes where the play is fetched from: the Apple TVs are on more than one network, or on one
 * the page is not on while the server could not route the play there. When the play is already routed to
 * their network — the usual case — Safari's picker alone is the way in, and a list first would make the
 * person pick their TV twice.
 */
export function needsReceiverChoice(receivers: Receiver[], playUrl: string, pageHost: string): boolean {
  const bases = new Set<string>();
  for (const r of receivers) {
    if (r.model?.startsWith('AppleTV') && r.base) bases.add(r.base);
  }
  if (bases.size > 1) return true; // TVs on different networks: only the person can choose
  if (bases.size === 0) return false;
  if (playBase(playUrl)) return false; // the server routed the play: the picker sends it as it is
  try {
    return new URL([...bases][0]).host !== pageHost; // a TV that the page's own address cannot reach
  } catch {
    return false;
  }
}
