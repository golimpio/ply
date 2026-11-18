<script lang="ts">
  // Official ply 3-letter wordmark (#2): Optical Sans lowercase "ply" with a rounded orange play triangle inside "p".
  let { variant = 'header' }: { variant?: 'header' | 'gate' } = $props();
  const gradId = $props.id();
</script>

<svg
  class="ply-logo"
  class:gate={variant === 'gate'}
  viewBox="0 0 698 488"
  aria-hidden="true"
  focusable="false"
>
  <defs>
    <linearGradient id={gradId} x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="var(--brand-play-start)" />
      <stop offset="100%" stop-color="var(--brand-play-end)" />
    </linearGradient>
  </defs>
  <!-- p -->
  <path
    fill="currentColor"
    d="M 0,109 Q 0,97 12,97 L 66,97 Q 78,97 78,109 L 78,115 C 100,98 128,93 158,93 C 234,93 292,151 292,230.5 C 292,310 234,368 158,368 C 128,368 100,363 78,346 L 78,475 Q 78,487 66,487 L 12,487 Q 0,487 0,475 Z"
  />
  <!-- rounded play triangle inside p -->
  <path
    fill={`url(#${gradId})`}
    d="M 84.00,169.00 A 18 18 0 0 1 110.43,153.10 L 226.43,214.60 A 18 18 0 0 1 226.43,246.40 L 110.43,307.90 A 18 18 0 0 1 84.00,292.00 Z"
  />
  <!-- l -->
  <rect fill="currentColor" x="318" y="0" width="78" height="364" rx="12" />
  <!-- straight-tail y -->
  <path
    fill="currentColor"
    d="M 422,112 Q 418,97 433,97 L 486,97 Q 497,97 502,108 L 556,241 Q 559,247 562,241 L 616,108 Q 621,97 632,97 L 685,97 Q 700,97 695,112 L 548,475 Q 543,487 531,487 L 478,487 Q 464,487 469,473 L 517,361 Z"
  />
</svg>

<style>
  .ply-logo {
    display: block;
    height: var(--brand-logo-h);
    width: auto;
  }
  .ply-logo.gate {
    height: var(--brand-gate-h);
  }
</style>
