// The page's projection light: the dominant colour of whatever film the
// current page is about. Pages call useAmbient() during init; the colour
// eases back to the house lights when they unmount.

export const ambient = $state({ color: '' });

const HEX = /^#[0-9a-f]{6}$/i;

/** Tint the page with the colour returned by `pick` while the calling component is mounted. */
export function useAmbient(pick: () => string | undefined | null): void {
  $effect(() => {
    const c = pick();
    ambient.color = c && HEX.test(c) ? c : '';
  });
  $effect(() => () => {
    ambient.color = '';
  });
}
