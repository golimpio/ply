<script lang="ts" module>
  // 24×24 stroke icons, drawn for this UI.
  const PATHS = {
    play: 'M8 5.5v13l11-6.5z',
    pause: 'M8 5h3v14H8zM13 5h3v14h-3z',
    search: 'M10.5 4a6.5 6.5 0 1 1 0 13 6.5 6.5 0 0 1 0-13zM15.5 15.5 20 20',
    home: 'M4 11 12 4l8 7M6.5 9.5V20h11V9.5',
    film: 'M4 5h16v14H4zM8 5v14M16 5v14M4 9.5h4M4 14.5h4M16 9.5h4M16 14.5h4',
    tv: 'M3.5 6h17v11h-17zM8.5 20h7M12 17v3',
    rescan: 'M19 12a7 7 0 1 1-2.05-4.95M19 4.5V8h-3.5',
    close: 'M6 6l12 12M18 6 6 18',
    left: 'M15 5l-7 7 7 7',
    right: 'M9 5l7 7-7 7',
    info: 'M12 3.5a8.5 8.5 0 1 1 0 17 8.5 8.5 0 0 1 0-17zM12 11v5.5M12 7.6v.4',
    back10: 'M5 12a7 7 0 1 0 2.05-4.95M5 4.5V8h3.5M10 10v5M13 10h2v5h-2z',
    fwd10: 'M19 12a7 7 0 1 1-2.05-4.95M19 4.5V8h-3.5M10 10v5M13 10h2v5h-2z',
    volume: 'M4 9.5h3.5L12 6v12l-4.5-3.5H4zM15.5 9a4 4 0 0 1 0 6M18 6.5a7.5 7.5 0 0 1 0 11',
    mute: 'M4 9.5h3.5L12 6v12l-4.5-3.5H4zM16 9.5l5 5M21 9.5l-5 5',
    fullscreen: 'M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5',
    exitfs: 'M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5',
    pip: 'M3.5 5.5h17v13h-17zM12.5 12h6v5h-6z',
    cc: 'M3.5 5.5h17v13h-17zM10.5 10.2a2.3 2.3 0 1 0 0 3.6M17 10.2a2.3 2.3 0 1 0 0 3.6',
    audio: 'M9 18V6l11-2v12M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0zM20 16a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z',
    airplay: 'M6 17H4.5V5h15v12H18M12 14.5l5 5.5H7z',
    more: 'M6 12h.01M12 12h.01M18 12h.01',
    star: 'M12 4l2.4 5 5.4.6-4 3.7 1.1 5.3L12 16l-4.9 2.6 1.1-5.3-4-3.7 5.4-.6z',
    settings:
      'M12 9a3 3 0 1 1 0 6 3 3 0 0 1 0-6zM19.4 13.5l1.6 1.2-2 3.4-1.9-.7a7.5 7.5 0 0 1-2.1 1.2L14.7 21h-4l-.3-2.4a7.5 7.5 0 0 1-2.1-1.2l-1.9.7-2-3.4 1.6-1.2a7.6 7.6 0 0 1 0-2.4L4.4 9.9l2-3.4 1.9.7a7.5 7.5 0 0 1 2.1-1.2l.3-2.4h4l.3 2.4a7.5 7.5 0 0 1 2.1 1.2l1.9-.7 2 3.4-1.6 1.2a7.6 7.6 0 0 1 0 2.4z',
    alert: 'M12 4 21 19.5H3zM12 10v4.5M12 17.2v.3',
    folder: 'M3.5 6.5h6l2 2h9v10h-17z',
    plus: 'M12 5v14M5 12h14',
    up: 'M12 19V5M6 11l6-6 6 6',
    down: 'M12 5v14M6 13l6 6 6-6',
    activity: 'M3 12h4l2.5-6.5 5 13 2.5-6.5h4',
    stop: 'M7 7h10v10H7z',
    check: 'M5 12.5l4.5 4.5L19 7.5',
  } as const;
  export type IconName = keyof typeof PATHS;
</script>

<script lang="ts">
  let { name, filled = false }: { name: IconName; filled?: boolean } = $props();
</script>

<svg
  viewBox="0 0 24 24"
  aria-hidden="true"
  fill={filled || name === 'play' || name === 'pause' ? 'currentColor' : 'none'}
  stroke="currentColor"
  stroke-width={name === 'play' || name === 'pause' ? 0 : name === 'more' ? 3.2 : 1.8}
  stroke-linecap="round"
  stroke-linejoin="round"><path d={PATHS[name]} /></svg>
