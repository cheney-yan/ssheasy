// PWA app-shell cache. Caches static assets so the installed app loads
// offline. Bump the version to invalidate old caches.
const CACHE_NAME = 'ssheasy-shell-v3';

self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);
  // Cache same-origin GETs only, and never the auth/login or websocket paths
  // (those must always hit the network so the login gate keeps working).
  if (event.request.method === 'GET' &&
      url.origin === self.location.origin &&
      !url.pathname.startsWith('/auth/') &&
      !url.pathname.startsWith('/p') &&
      !url.pathname.startsWith('/ws')) {
    event.respondWith(serveFromCache(event.request));
  }
});

// Cache that never stores auth redirects (resp.redirected) or error responses.
async function cachePut(cache, request, resp) {
  if (resp && resp.ok && resp.type === 'basic' && !resp.redirected) {
    cache.put(request, resp.clone());
  }
  return resp;
}

// Network-first: when online the freshest assets always win, so index.html and
// main.wasm can never drift out of sync after a redeploy. The cache is only a
// fallback for offline use (and the auth gate still redirects to /auth/login
// while online, since navigations hit the network first).
async function serveFromCache(request) {
  const cache = await caches.open(CACHE_NAME);
  try {
    return await cachePut(cache, request, await fetch(request));
  } catch (e) {
    return (await cache.match(request)) ||
           (request.mode === 'navigate'
             ? (await cache.match('/index.html')) || (await cache.match('/'))
             : null) ||
           new Response('Offline', { status: 503, statusText: 'Offline' });
  }
}

// Activate a freshly installed worker immediately.
self.addEventListener('install', event => {
  event.waitUntil(self.skipWaiting());
});

// Claim open pages and drop stale caches on activate.
self.addEventListener('activate', event => {
  event.waitUntil((async () => {
    const names = await caches.keys();
    await Promise.all(names.filter(n => n !== CACHE_NAME).map(n => caches.delete(n)));
    await self.clients.claim();
  })());
});
