import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { bringWindow, handoffBounds, handoffNotice, handoffWindow, landed, notifyCommand } from '../handoff.mjs';

test('puts the window in the bottom-right corner of his main screen, clamped to the screen', () => {
  assert.deepEqual(handoffBounds({ w: 1920, h: 1080 }), { left: 624, top: 244, width: 1280, height: 820, windowState: 'normal' });
  const small = handoffBounds({ w: 1000, h: 700 });
  assert.deepEqual([small.left, small.top, small.width, small.height], [84, 116, 900, 568]);
});

test('stays above the Dock and below the menu bar: uses the visible frame', () => {
  // 25 pt menu bar, 75 pt Dock at the bottom.
  const b = handoffBounds({ w: 1920, h: 1080, visible: { x: 0, y: 25, w: 1920, h: 980 } });
  assert.deepEqual([b.left, b.top, b.width, b.height], [624, 169, 1280, 820]);
  assert.ok(b.top + b.height <= 25 + 980);
  // Dock on the left.
  const l = handoffBounds({ w: 1920, h: 1080, visible: { x: 80, y: 25, w: 1840, h: 1055 } });
  assert.equal(l.left + l.width, 80 + 1840 - 16);
});

test('a move only counts when Chrome reads back the requested frame', () => {
  const want = { left: 624, top: 244, width: 1280, height: 820, windowState: 'normal' };
  assert.ok(landed({ ...want, left: 625.5 }, want));
  assert.ok(!landed({ ...want, left: 0, top: 0, width: 1920, height: 1080, windowState: 'fullscreen' }, want));
  assert.ok(!landed({ ...want, width: 900 }, want));
  assert.ok(!landed(null, want));
});

test('handing over only repositions the window: never activates, focuses, or sends input', async () => {
  const calls = [];
  let placed;
  const send = async (method, params) => {
    calls.push([method, params]);
    if (method === 'Browser.getWindowForTarget') return { windowId: 7 };
    if (method === 'Browser.setWindowBounds') placed = params.bounds;
    return method === 'Browser.getWindowBounds' ? { bounds: placed } : {};
  };
  const r = await handoffWindow(send, 'T1', { w: 1920, h: 1080 });
  assert.equal(r.windowId, 7);
  assert.deepEqual(calls.map(c => c[0]), ['Browser.getWindowForTarget', 'Browser.setWindowBounds', 'Browser.getWindowBounds']);
  assert.deepEqual(calls[1][1], { windowId: 7, bounds: r.bounds });
  assert.equal(r.verified, true);
});

test('an unverifiable move is reported, not hidden (e.g. stop pressed between move and read-back)', async () => {
  const send = async method => {
    if (method === 'Browser.getWindowBounds') throw new Error('stopped');
    return method === 'Browser.getWindowForTarget' ? { windowId: 7 } : {};
  };
  const r = await handoffWindow(send, 'T1', { w: 1920, h: 1080 });
  assert.deepEqual([r.verified, r.actual], [false, null]);
});

test('activation is a separate step, sent only by bring', async () => {
  const calls = [];
  await bringWindow(async m => { calls.push(m); return {}; }, 'T1');
  assert.deepEqual(calls, ['Target.activateTarget']);
});

test('with terminal-notifier, clicking the notification runs bring for that target', () => {
  const [bin, args] = notifyCommand(handoffNotice('请登录 Reddit'), {
    notifier: '/opt/homebrew/bin/terminal-notifier', node: '/usr/local/bin/node', script: "/Apps/O'vd/vd.mjs", targetId: 'T1',
    env: { OFFICE_VD_HOME: '/h', OFFICE_VD_PORT: undefined },
  });
  assert.equal(bin, '/opt/homebrew/bin/terminal-notifier');
  assert.match(args[args.indexOf('-message') + 1], /^请登录 Reddit。.*没抢焦点.*点这条通知/);
  assert.equal(args[args.indexOf('-execute') + 1], `OFFICE_VD_HOME='/h' '/usr/local/bin/node' '/Apps/O'\\''vd/vd.mjs' bring 'T1'`);
});

test('without terminal-notifier, the notice tells him to switch himself (osascript cannot run a click)', () => {
  const [bin, args] = notifyCommand(handoffNotice(), { node: 'n', script: 's', targetId: 'T1' });
  assert.equal(bin, 'osascript');
  assert.match(args[1], /请在这页操作。.*Cmd-Tab/);
  assert.ok(!args.join(' ').includes('bring'));
});

test('vd.mjs no longer activates on look, keeps bring behind the stop gate, and opens Preview only on request', () => {
  const src = fs.readFileSync(new URL('../vd.mjs', import.meta.url), 'utf8');
  assert.ok(!src.includes('Target.activateTarget'));
  assert.match(src, /\['state', 'shot', 'peek'\]\.includes\(cmd\)/);
  assert.match(src, /PEEK_OPEN === '1'/);
});
