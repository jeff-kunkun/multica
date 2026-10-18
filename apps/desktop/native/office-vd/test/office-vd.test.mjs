// node --test apps/desktop/native/office-vd/test/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs'; import os from 'node:os'; import path from 'node:path';
import { parseLatch, readLatch, stop, resume, latchPath } from '../latch.mjs';
import { gate, startBroker, READ_ONLY_WHILE_STOPPED } from '../broker.mjs';

const fixtures = JSON.parse(fs.readFileSync(new URL('./latch-fixtures.json', import.meta.url), 'utf8'));
const tmpHome = () => fs.mkdtempSync(path.join(os.tmpdir(), 'office-vd-test-'));
const quiet = () => {};

for (const f of fixtures) {
  test(`latch fixture: ${f.name}`, () => {
    const l = parseLatch(f.raw);
    assert.equal(l.state, f.state);
    assert.equal(l.generation, f.generation);
    assert.equal(Boolean(l.readError), f.readError);
  });
}

test('stop persists, survives a re-read, and is idempotent', () => {
  const home = tmpHome();
  assert.equal(readLatch(home).state, 'running');
  const s = stop({ home, source: 'test' });
  assert.deepEqual([s.state, s.generation], ['stopped', 1]);
  assert.equal(stop({ home }).generation, 1, 'a second stop keeps the generation');
  assert.equal(readLatch(home).state, 'stopped');
  assert.equal(fs.statSync(latchPath(home)).mode & 0o777, 0o600);
});

test('resume needs the generation it saw; a stale resume cannot undo a newer stop', () => {
  const home = tmpHome();
  const first = stop({ home });
  const r1 = resume({ home, expectGeneration: first.generation });
  assert.equal(r1.ok, true);
  assert.deepEqual([r1.latch.state, r1.latch.generation], ['running', 2]);
  const second = stop({ home });
  assert.equal(second.generation, 3);
  const stale = resume({ home, expectGeneration: first.generation });
  assert.equal(stale.ok, false);
  assert.equal(readLatch(home).state, 'stopped');
  assert.equal(resume({ home, expectGeneration: 99 }).ok, false);
});

test('an unreadable latch fails closed and the next stop moves past it', () => {
  const home = tmpHome();
  fs.mkdirSync(path.dirname(latchPath(home)), { recursive: true });
  fs.writeFileSync(latchPath(home), '{"version":1,"state":"runn');
  assert.equal(readLatch(home).state, 'stopped');
  assert.equal(resume({ home, expectGeneration: null }).ok, false, 'cannot resume what cannot be read');
  const s = stop({ home });
  assert.equal(s.state, 'stopped');
  assert.ok(s.generation >= 1);
  assert.equal(readLatch(home).readError, undefined);
});

test('gate: stopped lets only read-only methods through', () => {
  const stopped = { state: 'stopped', generation: 4 };
  for (const m of ['Input.dispatchMouseEvent', 'Input.insertText', 'Runtime.evaluate', 'Page.navigate',
    'Target.createTarget', 'Target.activateTarget', 'Browser.setWindowBounds', 'Target.closeTarget']) {
    assert.equal(gate(stopped, { method: m })?.code, 423, m);
  }
  for (const m of READ_ONLY_WHILE_STOPPED) assert.equal(gate(stopped, { method: m }), null, m);
  assert.equal(gate({ state: 'stopped', generation: null, readError: 'x' }, { method: 'Runtime.evaluate' })?.code, 423);
});

test('gate: an older generation is refused even while running', () => {
  const running = { state: 'running', generation: 6 };
  assert.equal(gate(running, { method: 'Runtime.evaluate', generation: 6 }), null);
  assert.equal(gate(running, { method: 'Runtime.evaluate' }), null);
  assert.equal(gate(running, { method: 'Runtime.evaluate', generation: 4 })?.code, 409);
  assert.equal(gate(running, { method: 'not a method' })?.code, 400);
  assert.equal(gate(running, { method: '__proto__.x' })?.code, 400);
});

test('broker refuses browser origins, and keeps refusing input after a restart', async () => {
  const home = tmpHome();
  const opts = { home, port: 0, endpointFiles: [path.join(home, 'none')], reconnectMs: 60000, log: quiet };
  let b = await startBroker(opts);
  const post = (port, body, headers = {}) => fetch(`http://127.0.0.1:${port}/`, { method: 'POST', body: JSON.stringify(body), headers });
  try {
    let r = await post(b.port, { method: 'Runtime.evaluate' }, { origin: 'https://evil.example' });
    assert.equal(r.status, 403);
    r = await post(b.port, { method: 'Runtime.evaluate' });
    assert.equal(r.status, 503, 'running, gate passes, Chrome not connected');
    stop({ home, source: 'test' });
    r = await post(b.port, { method: 'Input.dispatchKeyEvent' });
    assert.equal(r.status, 423);
    const h = await (await fetch(`http://127.0.0.1:${b.port}/health`)).json();
    assert.deepEqual(h.latch, { state: 'stopped', generation: 1 });
  } finally { await b.close(); }
  b = await startBroker(opts);
  try {
    const r = await post(b.port, { method: 'Input.dispatchKeyEvent' });
    assert.equal(r.status, 423, 'restart does not resume');
    const s = await (await fetch(`http://127.0.0.1:${b.port}/control/state`)).json();
    assert.equal(s.latch.state, 'stopped');
  } finally { await b.close(); }
});
