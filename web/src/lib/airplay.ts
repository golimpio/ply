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
