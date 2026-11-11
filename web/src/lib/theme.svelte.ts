// Cinema Dark ('dark', the default) and Warm Paper ('light') theme toggle.
const KEY = 'ply.theme';

export type Theme = 'dark' | 'light';

function readInitial(): Theme {
  try {
    return localStorage.getItem(KEY) === 'light' ? 'light' : 'dark';
  } catch {
    return 'dark';
  }
}

function apply(next: Theme) {
  if (typeof document === 'undefined') return;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (next === 'light') {
    document.documentElement.dataset.theme = 'light';
    meta?.setAttribute('content', '#f6f4ef');
  } else {
    delete document.documentElement.dataset.theme;
    meta?.setAttribute('content', '#0c0c0b');
  }
}

const initial = readInitial();
apply(initial);

export const theme = $state<{ current: Theme }>({ current: initial });

export function toggleTheme() {
  const next: Theme = theme.current === 'dark' ? 'light' : 'dark';
  theme.current = next;
  apply(next);
  try {
    localStorage.setItem(KEY, next);
  } catch {
    // storage unavailable: applied for this session only
  }
}
