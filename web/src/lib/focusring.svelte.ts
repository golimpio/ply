// Focus ring preference: off by default, persisted in localStorage.
const KEY = 'ply.focus_ring';

function readInitial(): boolean {
  if (typeof localStorage === 'undefined') return false;
  try {
    return localStorage.getItem(KEY) === 'true';
  } catch {
    return false;
  }
}

function apply(enabled: boolean) {
  if (typeof document === 'undefined') return;
  if (enabled) {
    document.documentElement.dataset.focusRing = 'true';
  } else {
    delete document.documentElement.dataset.focusRing;
  }
}

const initial = readInitial();
apply(initial);

export const focusRing = $state<{ enabled: boolean }>({ enabled: initial });

export function setFocusRing(enabled: boolean) {
  focusRing.enabled = enabled;
  apply(enabled);
  try {
    if (enabled) {
      localStorage.setItem(KEY, 'true');
    } else {
      localStorage.removeItem(KEY);
    }
  } catch {
    // storage unavailable: applied for this session only
  }
}

export function toggleFocusRing() {
  setFocusRing(!focusRing.enabled);
}
