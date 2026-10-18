import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { bringWindow, handoffBounds, handoffNotice, handoffWindow, notifyCommand } from '../handoff.mjs';

test('puts the window in the bottom-right corner of his main screen, clamped to the screen', () => {
  assert.deepEqual(handoffBounds({ w: 1920, h: 1080 }), { left: 624, top: 244, width: 1280, height: 820, windowState: 'normal' });
  const small = handoffBounds({ w: 1000, h: 700 });
  assert.deepEqual([small.left, small.top, small.width, small.height], [84, 84, 900, 600]);
});

test('handing over only repositions the window: never activates, focuses, or sends input', async () => {
  const calls = [];
  const send = async (method, params) => { calls.push([method, params]); return method === 'Browser.getWindowForTarget' ? { windowId: 7 } : {}; };
  const r = await handoffWindow(send, 'T1', { w: 1920, h: 1080 });
  assert.equal(r.windowId, 7);
  assert.deepEqual(calls.map(c => c[0]), ['Browser.getWindowForTarget', 'Browser.setWindowBounds']);
  assert.deepEqual(calls[1][1], { windowId: 7, bounds: r.bounds });
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
