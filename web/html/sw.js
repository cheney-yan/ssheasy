// Store client configurations in a map
// Structure: { clientId: { sshClientID: string, host: string  } }
let clientMap = {};
var mainClient;

// PWA app-shell cache. This worker's primary job is tunneling fetches through
// SSH (below); on top of that we cache static assets so the installed app can
// load offline. Bump the version to invalidate old caches.
const CACHE_NAME = 'ssheasy-shell-v2';

self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);
  let clientId = event.clientId;
  
  console.log("fetch intercepted: " + url + ", clientId: " + clientId + ' replaces client ' + event.replacesClientId + ' resulting client id:' + event.resultingClientId);
  
  if (!clientId && event.resultingClientId) {
    clientId = event.resultingClientId;
  }

  if (!clientId) {
    return;
  }

  if (url.pathname.includes('/favicon.ico')) {
    return;
  }

  if(url.pathname.includes('/registerportforward')) {
    console.log('received registerportforward request from client ' + clientId);
    event.respondWith( new Response(event.clientId, { status: 200 }));
    mainClient = clientId;
    return 
  }

  if(url.pathname.includes('/portforward')) {
    let pathArray = url.pathname.split('/');
    let sshClientID = null;
    let targetHost = null;
    // portforward path should contain id of the window running the ssh client and the host to target
    // eg. /portforward/11d62b68-cefa-4014-9dbb-433daba89268/localhost:8080
    for (i = 0; i < pathArray.length-2; i++) {
      if(pathArray[i] == "portforward") {
        sshClientID = pathArray[i+1];
        targetHost = pathArray.slice(i+2).join('/');
        break;
      }
    }
    if (sshClientID && targetHost) {
      clientMap[clientId] = { sshClientID: sshClientID, host: targetHost };
      console.log('sw: new tunnel client registered, id=' + clientId + ", ssh client id: " + sshClientID + ", target host: " + targetHost);
    }
  }

  let clientConfig = null;

  // Check if this client ID is already known
  if (clientMap[clientId]) {
    // Client already registered, serve according to its stored classification
    clientConfig = clientMap[clientId];
    console.log('sw: found client with id: ' + clientId) ;
    
    if (event.resultingClientId != null && event.resultingClientId != "") {
      console.log('sw: client with resulting client id:' + event.resultingClientId +' added to the clients') ;
      clientMap[event.resultingClientId] = clientConfig;
    } 
  }else if (clientMap[event.replacesClientId]){
    clientConfig = clientMap[vent.replacesClientId];
    console.log('sw: client ' + clientId + ' replaces client ' + event.replacesClientId);
    clientMap[event.replacesClientId] = clientConfig;
  } 
  if (clientConfig) {
      console.log('sw: tunneling request');
      event.respondWith(handlePortForwardedRequest(event, clientConfig));
      return;
  }

  // Not a tunneled request: apply app-shell caching so the installed PWA works
  // offline. Only same-origin GETs, and never the auth/login or websocket paths
  // (those must always hit the network so the login gate keeps working).
  if (event.request.method === 'GET' &&
      url.origin === self.location.origin &&
      !url.pathname.startsWith('/auth/') &&
      !url.pathname.startsWith('/p') &&
      !url.pathname.startsWith('/ws') &&
      !url.pathname.includes('portforward')) {
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

// Network-first for everything: when online the freshest assets always win, so
// index.html and main.wasm can never drift out of sync after a redeploy. The
// cache is only a fallback for offline use (and the auth gate still redirects
// to /auth/login while online, since navigations hit the network first).
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

// Store pending responses for FETCH_INTERCEPT_RESPONSE messages
let pendingResponses = {};

// Receive messages from clients to register main clients or handle responses
self.addEventListener('message', event => {
    if (event.data?.type === 'FETCH_INTERCEPT_RESPONSE') {
    const id = event.data.id;
    if (pendingResponses[id]) {
      pendingResponses[id].resolve(event.data.response);
      delete pendingResponses[id];
    }
  }
});

async function handlePortForwardedRequest(event, clientConfig) {
  const reqId = Math.random().toString(36).slice(2);

  const reqData = {
    id: reqId,
    host: clientConfig.host,
    sshClientID: clientConfig.sshClientID,
    method: event.request.method,
    url: event.request.url,
    headers: Object.fromEntries(event.request.headers.entries()),
    body: (event.request.method !== 'GET' && event.request.method !== 'HEAD')
      ? await event.request.text()
      : null,
  };

  const client = await self.clients.get(clientConfig.sshClientID);
  console.log('sw: sending FETCH_INTERCEPT to ssh client: ' + clientConfig.sshClientID + 'with id=' + reqId);
  client.postMessage({ type: 'FETCH_INTERCEPT', data: reqData });

  const responseData = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      console.log('sw: timeout waiting for FETCH_INTERCEPT_RESPONSE for id=' + reqId);
      delete pendingResponses[reqId];
      reject(new Error('Client response timeout'));
    }, 30000); // 30 second timeout

    pendingResponses[reqId] = { resolve, reject };
  }).catch(err => {
    console.error('sw: error waiting for response:', err);
    throw err;
  });

  return new Response(responseData.body, {
    status: responseData.status,
    headers: responseData.headers,
  });
}
