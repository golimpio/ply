// Browser checks for the player chrome (review findings W5-W7) against a running ply.
//
// Needs Google Chrome (Playwright's bundled Chromium lacks H.264/AAC) and a ply
// serving an item with two audio tracks and a subtitle track, e.g.:
//
//   printf '1\n00:00:01,000 --> 00:00:20,000\nHello\n' > sub.srt
//   ffmpeg -f lavfi -i testsrc2=s=1280x720:r=24:d=40 -f lavfi -i sine=f=440:d=40 \
//     -f lavfi -i sine=f=660:d=40 -i sub.srt -map 0 -map 1 -map 2 -map 3 \
//     -c:v libx264 -preset ultrafast -g 48 -pix_fmt yuv420p -c:a aac -c:s srt \
//     "media/Films/Two Tracks (2020).mkv"
//   ply -media media -addr 127.0.0.1:8472
//   PLY_E2E_URL=http://127.0.0.1:8472 npm run e2e
//
// Each check was confirmed to fail against the UI before the fixes (1282ff3).
import { chromium } from 'playwright-core';

const base = process.env.PLY_E2E_URL ?? 'http://127.0.0.1:8472';
const results = [];
const check = (name, ok, detail = '') => results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);

const items = await (await fetch(`${base}/api/items`)).json();
let id;
for (const it of items) {
  const d = await (await fetch(`${base}/api/items/${it.id}`)).json();
  const audio = d.streams.filter((s) => s.type === 'audio').length;
  if (audio >= 2 && d.subtitles.length >= 1) {
    id = it.id;
    break;
  }
}
if (!id) {
  console.error('No item with two audio tracks and a subtitle; see the header for the fixture.');
  process.exit(2);
}

const fKeydown = (page) =>
  page.evaluate(
    () =>
      new Promise((resolve) => {
        const on = (e) => {
          if (e.key.toLowerCase() !== 'f') return; // the modifier's own keydown comes first
          window.removeEventListener('keydown', on);
          setTimeout(() => resolve({ prevented: e.defaultPrevented, fs: !!document.fullscreenElement }), 0);
        };
        window.addEventListener('keydown', on);
      }),
  );
const opacity = (page) => page.evaluate(() => getComputedStyle(document.querySelector('.controls .bottom')).opacity);

const browser = await chromium.launch({ channel: 'chrome', headless: true });
try {
  // W5: on phones every control and the scrubber stay inside the frame, and the
  // controls that matter (Full screen, subtitles, audio) are always there.
  for (const [w, h] of [[360, 740], [320, 568]]) {
    const page = await browser.newPage({ viewport: { width: w, height: h } });
    await page.goto(`${base}/#/play/${id}`);
    await page.waitForSelector('.controls .row button', { timeout: 15000 });
    await page.waitForTimeout(1500);
    const r = await page.evaluate(() => {
      const frame = document.querySelector('.frame').getBoundingClientRect();
      const shown = [...document.querySelectorAll('.controls .row button')].filter((b) => b.offsetParent !== null);
      const scr = document.querySelector('.scrubber input').getBoundingClientRect();
      return {
        labels: shown.map((b) => b.getAttribute('aria-label')),
        clipped: shown.filter((b) => b.getBoundingClientRect().right > frame.right + 0.5).map((b) => b.getAttribute('aria-label')),
        scrubberOut: scr.right > frame.right + 0.5,
      };
    });
    check(`W5 ${w}x${h}: controls inside the frame`, !r.clipped.length && !r.scrubberOut, r.clipped.length ? `clipped: ${r.clipped}` : '');
    check(`W5 ${w}x${h}: Full screen, Subtitles and Audio track shown`, ['Full screen', 'Subtitles', 'Audio track'].every((l) => r.labels.includes(l)), r.labels.join(', '));
    await page.close();
  }

  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  await page.goto(`${base}/#/play/${id}`);
  await page.waitForFunction(() => { const v = document.querySelector('video'); return v && v.readyState >= 1 && v.duration > 0; }, null, { timeout: 20000 });

  // W6: Space activates a focused button instead of toggling playback.
  await page.evaluate(async () => { const v = document.querySelector('video'); v.pause(); v.currentTime = 25; await new Promise((r) => v.addEventListener('seeked', r, { once: true })); });
  await page.focus('button[aria-label="Back 10 seconds"]');
  await page.keyboard.press('Space');
  await page.waitForTimeout(1200);
  const a = await page.evaluate(() => { const v = document.querySelector('video'); return { paused: v.paused, t: v.currentTime }; });
  check('W6 Space on "Back 10 seconds" seeks and stays paused', a.paused && a.t > 12 && a.t < 18, `paused=${a.paused} t=${a.t.toFixed(1)}`);

  // W6: Ctrl/Cmd shortcuts belong to the browser, even with the scrubber focused.
  await page.focus('.scrubber input');
  const pending = fKeydown(page);
  await page.keyboard.press('Control+f');
  const c = await pending;
  check('W6 Ctrl+F with the scrubber focused reaches the browser', !c.prevented && !c.fs, JSON.stringify(c));

  // W6: a held toggle key acts once.
  await page.evaluate(() => { document.activeElement?.blur(); document.querySelector('video').muted = false; });
  await page.keyboard.press('m');
  for (let i = 0; i < 3; i++) await page.evaluate(() => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'm', repeat: true, bubbles: true })));
  const muted = await page.evaluate(() => document.querySelector('video').muted);
  check('W6 a held "m" mutes once', muted === true, `muted=${muted}`);

  // W7: keyboard focus brings hidden controls back and keeps them visible.
  await page.evaluate(() => { const v = document.querySelector('video'); v.muted = true; return v.play(); });
  await page.mouse.move(640, 300);
  await page.waitForTimeout(3600);
  const before = await opacity(page);
  await page.keyboard.press('Tab');
  await page.waitForTimeout(450);
  const shown = await opacity(page);
  await page.waitForTimeout(3200);
  const kept = await opacity(page);
  check('W7 control: controls auto-hide while playing', before === '0', `opacity=${before}`);
  check('W7 Tab into hidden controls shows them', shown === '1', `opacity=${shown}`);
  check('W7 they stay while keyboard focus is inside', kept === '1', `opacity=${kept}`);
} finally {
  await browser.close();
}
console.log(results.join('\n'));
process.exit(results.some((r) => r.startsWith('FAIL')) ? 1 : 0);
