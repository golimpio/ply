// ply service worker: app shell only.
// - index.html (navigations): network first, so a rebuilt server binary never
//   serves a stale UI; the cached copy is only an offline fallback.
// - /assets/* (content-hashed by Vite): cache first, they never change.
// - /api/* and media: never touched; the browser talks to the server directly.
const CACHE = 'ply-shell-v1';

self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      for (const key of await caches.keys()) if (key !== CACHE) await caches.delete(key);
      await self.clients.claim();
    })(),
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin || url.pathname.startsWith('/api/')) return;

  if (req.mode === 'navigate') {
    event.respondWith(networkFirst(req));
  } else if (url.pathname.startsWith('/assets/')) {
    event.respondWith(cacheFirst(req));
  }
});

async function networkFirst(req) {
  const cache = await caches.open(CACHE);
  try {
    const res = await fetch(req);
    if (res.ok) await cache.put('/index.html', res.clone());
    return res;
  } catch (err) {
    const cached = await cache.match('/index.html');
    if (cached) return cached;
    throw err;
  }
}

async function cacheFirst(req) {
  const cache = await caches.open(CACHE);
  const hit = await cache.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) await cache.put(req, res.clone());
  return res;
}
