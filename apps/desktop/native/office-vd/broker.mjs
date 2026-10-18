// broker.mjs: holds ONE CDP connection to Kun's everyday Chrome so Chrome asks
// "Allow remote debugging?" once, not on every agent command.
// Agents call it over loopback HTTP:  POST http://127.0.0.1:9339/  {"method","params","sessionId","generation"?}
//                                      GET  http://127.0.0.1:9339/health
//                                      GET  http://127.0.0.1:9339/control/state
// Never opens a second handshake while one is pending, so prompts cannot stack.
//
// Input gate: every request re-reads the stop latch (latch.mjs). While it says stopped, only the
// read-only methods below pass, so the office can still show the agents' pages but nothing is typed,
// clicked, navigated or evaluated. The latch is a file, so the gate holds across broker restarts and
// does not depend on the network. A request that names an older generation is refused even when running.
import http from 'http';
import fs from 'fs'; import os from 'os'; import path from 'path'; import { pathToFileURL } from 'url';
import { readLatch, vdHome as defaultHome, writeDurable } from './latch.mjs';

export const BROKER_PROTOCOL = 2;
export const READ_ONLY_WHILE_STOPPED = new Set([
  'Browser.getVersion',
  'Browser.getWindowForTarget',
  'Page.captureScreenshot',
  'Target.attachToTarget',
  'Target.detachFromTarget',
  'Target.getTargets',
]);

export function ownedTargetsPath(home) { return path.join(home, 'control', 'owned-targets.json'); }

export function readOwnedTargets(home) {
  try {
    const v = JSON.parse(fs.readFileSync(ownedTargetsPath(home), 'utf8'));
    return Array.isArray(v.targets) ? v.targets.filter(t => t && typeof t.targetId === 'string') : [];
  } catch { return []; }
}

function writeOwnedTargets(home, targets) {
  writeDurable(ownedTargetsPath(home), JSON.stringify({ version: 1, targets }) + '\n');
}

// gate decides whether one request may reach Chrome. Pure, so the rules are testable without Chrome.
export function gate(latch, q) {
  if (!q || typeof q.method !== 'string' || !/^[A-Za-z][A-Za-z0-9]*\.[A-Za-z][A-Za-z0-9]*$/.test(q.method)) {
    return { code: 400, body: { error: 'invalid CDP method', code: 'invalid_method' } };
  }
  if (latch.state !== 'running' && !READ_ONLY_WHILE_STOPPED.has(q.method)) {
    return { code: 423, body: { error: `${q.method}: office agents are stopped on this Mac; nothing runs until Kun allows new tasks`, code: 'stopped', generation: latch.generation } };
  }
  if (q.generation !== undefined && q.generation !== latch.generation) {
    return { code: 409, body: { error: `${q.method}: generation ${q.generation} is no longer current (${latch.generation})`, code: 'stale_generation', generation: latch.generation } };
  }
  return null;
}

export function startBroker({
  home = defaultHome(),
  port = Number(process.env.OFFICE_VD_PORT || 9339),
  endpointFiles = process.env.OFFICE_VD_DEVTOOLS_ENDPOINT
    ? [process.env.OFFICE_VD_DEVTOOLS_ENDPOINT]
    // launchd jobs may not read Chrome's data folder (macOS app-data protection), so vd.mjs mirrors
    // DevToolsActivePort into <vd home>/devtools-endpoint; read that first.
    : [path.join(home, 'devtools-endpoint'), path.join(os.homedir(), 'Library/Application Support/Google/Chrome/DevToolsActivePort')],
  reconnectMs = 30000,
  log = (...a) => console.log(new Date().toISOString(), ...a),
} = {}) {
  let ws = null, ready = false, id = 0, closing = false, lastLatch = null, retry = null;
  const pend = {};

  const latch = () => {
    const l = readLatch(home);
    const key = `${l.state}:${l.generation}`;
    if (key !== lastLatch) { log(`latch ${key}${l.readError ? ` (${l.readError}, failing closed)` : ''}`); lastLatch = key; }
    return l;
  };

  const cdp = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    if (!ready) return reject(new Error('not connected'));
    const i = ++id;
    const t = setTimeout(() => { delete pend[i]; reject(new Error(`${method}: timeout`)); }, 30000);
    pend[i] = m => { clearTimeout(t); m.error ? reject(new Error(`${method}: ${m.error.message}`)) : resolve(m.result); };
    ws.send(JSON.stringify({ id: i, method, params, sessionId }));
  });

  // Windows the agents opened through this broker. The screen capture helper only shows windows it can
  // account for here, so a window nobody registered stops the picture instead of leaking into it.
  const track = async (q, result) => {
    if (q.method === 'Target.createTarget' && q.params?.newWindow && result?.targetId) {
      let windowId = null;
      try { windowId = (await cdp('Browser.getWindowForTarget', { targetId: result.targetId })).windowId; } catch {}
      writeOwnedTargets(home, [...readOwnedTargets(home).filter(t => t.targetId !== result.targetId),
        { targetId: result.targetId, windowId, createdAt: new Date().toISOString() }]);
    } else if (q.method === 'Target.closeTarget' && q.params?.targetId) {
      writeOwnedTargets(home, readOwnedTargets(home).filter(t => t.targetId !== q.params.targetId));
    }
  };

  const prune = async () => {
    if (!ready) return;
    try {
      const live = new Set((await cdp('Target.getTargets')).targetInfos.map(t => t.targetId));
      const owned = readOwnedTargets(home);
      const kept = owned.filter(t => live.has(t.targetId));
      if (kept.length !== owned.length) writeOwnedTargets(home, kept);
    } catch {}
  };

  function connect() {
    if (closing) return;
    let devPort, devPath;
    for (const f of endpointFiles) { try { [devPort, devPath] = fs.readFileSync(f, 'utf8').trim().split('\n'); break; } catch {} }
    if (!devPath) { log(`no DevTools endpoint yet; retry in ${reconnectMs / 1000}s`); retry = setTimeout(connect, reconnectMs); return; }
    log(`connecting ws://127.0.0.1:${devPort}${devPath} (Chrome may ask Kun once)`);
    ws = new WebSocket(`ws://127.0.0.1:${devPort}${devPath}`);
    ws.onopen = () => { ready = true; log('connected'); prune(); };
    ws.onmessage = e => { const m = JSON.parse(e.data); if (m.id && pend[m.id]) { pend[m.id](m); delete pend[m.id]; } };
    ws.onclose = () => {
      ready = false; ws = null; log(`closed; reconnect in ${reconnectMs / 1000}s`);
      for (const k in pend) { pend[k]({ error: { message: 'chrome connection closed' } }); delete pend[k]; }
      if (!closing) retry = setTimeout(connect, reconnectMs);
    };
    ws.onerror = () => {};
  }

  const server = http.createServer((req, res) => {
    const reply = (code, obj) => { res.writeHead(code, { 'content-type': 'application/json' }); res.end(JSON.stringify(obj)); };
    // Browsers always send Origin on cross-site requests; agents and the desktop app never do.
    // Refusing it keeps any web page Kun opens from driving his Chrome through this port.
    if (req.headers.origin !== undefined) return reply(403, { error: 'browser requests are not accepted', code: 'origin_refused' });
    if (req.method === 'GET' && req.url === '/health') {
      const l = latch();
      return reply(200, { connected: ready, protocol: BROKER_PROTOCOL, latch: { state: l.state, generation: l.generation } });
    }
    if (req.method === 'GET' && req.url === '/control/state') {
      const l = latch();
      return reply(200, { protocol: BROKER_PROTOCOL, connected: ready, latch: l, inflight: Object.keys(pend).length, ownedTargets: readOwnedTargets(home) });
    }
    if (req.method !== 'POST') return reply(404, { error: 'POST {method, params, sessionId}' });
    let body = '';
    req.on('data', c => {
      body += c;
      if (body.length > 1024 * 1024) { reply(413, { error: 'request too large' }); req.destroy(); }
    });
    req.on('end', () => {
      let q; try { q = JSON.parse(body); } catch { return reply(400, { error: 'bad json' }); }
      const refused = gate(latch(), q);
      if (refused) return reply(refused.code, refused.body);
      if (!ready) return reply(503, { error: 'not connected to Chrome yet (waiting for Kun to click Allow once?)' });
      cdp(q.method, q.params || {}, q.sessionId).then(
        async r => { await track(q, r).catch(() => {}); reply(200, r); },
        e => reply(e.message.endsWith('timeout') ? 504 : 500, { error: e.message }),
      );
    });
  });

  const pruneTimer = setInterval(prune, 60000); pruneTimer.unref();
  return new Promise(resolve => server.listen(port, '127.0.0.1', () => {
    log(`broker on 127.0.0.1:${server.address().port}`);
    latch();
    connect();
    resolve({
      port: server.address().port,
      close: () => new Promise(r => { closing = true; clearInterval(pruneTimer); clearTimeout(retry); try { ws?.close(); } catch {} server.close(() => r()); }),
    });
  }));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) startBroker();
