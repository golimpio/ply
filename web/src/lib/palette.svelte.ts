// Open state of the search palette, shared by the header, the tab bar and the
// global keyboard shortcut.
export const palette = $state({ open: false });

export function openSearch(): void {
  palette.open = true;
}
