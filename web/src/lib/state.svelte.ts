import { api } from './api';

export const app = $state({
  serverName: '',
  /** Bumped when a scan finishes so pages re-fetch. */
  libraryVersion: 0,
});

export const scan = $state({
  running: false,
  message: '',
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
let messageTimer: ReturnType<typeof setTimeout> | undefined;

function say(message: string, clearAfterMs = 0) {
  scan.message = message;
  clearTimeout(messageTimer);
  if (clearAfterMs) messageTimer = setTimeout(() => (scan.message = ''), clearAfterMs);
}

/** Poll GET /api/scan until the running scan finishes, then refresh the library views. */
async function follow(): Promise<void> {
  scan.running = true;
  say('Scanning…');
  try {
    for (;;) {
      const s = await api.scanStatus();
      if (!s.running) {
        say(s.lastError ? `Scan failed: ${s.lastError}` : `Scan done · ${s.itemCount} items`, 8000);
        app.libraryVersion++;
        return;
      }
      await sleep(1000);
    }
  } catch (e) {
    say(e instanceof Error ? e.message : 'Scan status unavailable', 8000);
  } finally {
    scan.running = false;
  }
}

/** POST /api/scan (409 = already running) and follow it to completion. */
export async function rescan(): Promise<void> {
  if (scan.running) return;
  scan.running = true;
  try {
    await api.startScan();
  } catch (e) {
    scan.running = false;
    say(e instanceof Error ? e.message : 'Could not start scan', 8000);
    return;
  }
  await follow();
}

/** On load: attach to a scan already in progress (e.g. the periodic one). */
export async function attachToRunningScan(): Promise<void> {
  try {
    if ((await api.scanStatus()).running && !scan.running) await follow();
  } catch {
    // status is informational only
  }
}
