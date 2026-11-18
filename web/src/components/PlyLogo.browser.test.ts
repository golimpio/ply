// The official 3-letter ply wordmark SVG rendered in the header and token gate: verifies its
// geometry, gradient IDs across multiple instances, and token-driven sizing.
import '../styles/tokens.css';
import { flushSync, mount, unmount } from 'svelte';
import { describe, expect, it } from 'vitest';
import Header from './Header.svelte';
import PlyLogo from './PlyLogo.svelte';
import TokenPrompt from './TokenPrompt.svelte';

function mountLogo(variant?: 'header' | 'gate') {
  const target = document.body.appendChild(document.createElement('div'));
  const component = mount(PlyLogo, { target, props: variant ? { variant } : {} });
  flushSync();
  return {
    svg: target.querySelector<SVGSVGElement>('svg.ply-logo')!,
    close() {
      unmount(component);
      target.remove();
    },
  };
}

describe('PlyLogo', () => {
  it('renders the 3-letter vector mark (p, play triangle, l, y) with token-sized variants', () => {
    const header = mountLogo('header');
    const gate = mountLogo('gate');
    try {
      expect(header.svg).not.toBeNull();
      expect(header.svg.getAttribute('viewBox')).toBe('0 0 698 488');
      expect(header.svg.querySelectorAll('path')).toHaveLength(3);
      expect(header.svg.querySelectorAll('rect')).toHaveLength(1);

      // Gradient IDs are unique per instance so multiple marks on a page never collide.
      const headerGrad = header.svg.querySelector('linearGradient')?.id;
      const gateGrad = gate.svg.querySelector('linearGradient')?.id;
      expect(headerGrad).toBeTruthy();
      expect(gateGrad).toBeTruthy();
      expect(headerGrad).not.toBe(gateGrad);

      const headerH = parseFloat(getComputedStyle(header.svg).height);
      const gateH = parseFloat(getComputedStyle(gate.svg).height);
      expect(headerH).toBeGreaterThan(15);
      expect(gateH).toBeGreaterThan(headerH);
    } finally {
      header.close();
      gate.close();
    }
  });

  it('replaces the plain text brand in Header and TokenPrompt while preserving accessibility labels', () => {
    const target = document.body.appendChild(document.createElement('div'));
    const header = mount(Header, { target });
    const gateTarget = document.body.appendChild(document.createElement('div'));
    const gate = mount(TokenPrompt, { target: gateTarget, props: { onsave: () => {} } });
    flushSync();
    try {
      const brand = target.querySelector<HTMLAnchorElement>('a.brand')!;
      expect(brand.getAttribute('aria-label')).toBe('ply home');
      expect(brand.querySelector('svg.ply-logo')).not.toBeNull();
      expect(brand.textContent?.trim()).toBe('');

      const heading = gateTarget.querySelector('h1')!;
      expect(heading.querySelector('svg.ply-logo')).not.toBeNull();
      expect(heading.textContent?.trim()).toBe('ply');
    } finally {
      unmount(header);
      unmount(gate);
      target.remove();
      gateTarget.remove();
    }
  });
});
