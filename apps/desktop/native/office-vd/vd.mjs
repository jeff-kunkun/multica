// vd.mjs: office agents' browser on the virtual display, via Kun's everyday Chrome (CDP, 127.0.0.1).
//   node vd.mjs open <url>             -> new window on the virtual display; prints targetId
//   node vd.mjs shot <targetId> <png>  -> screenshot that tab
//   node vd.mjs look <targetId> <msg>  -> needs Kun: move that window to his main screen and notify; never focuses it
//   node vd.mjs bring <targetId>       -> Kun clicked the notification: now activate that window
//   node vd.mjs back <targetId>        -> move the window back to the virtual display
//   node vd.mjs close <targetId>
//   node vd.mjs state                  -> windows and visible tabs (for checks)
//   node vd.mjs peek                   -> screenshots of agent pages; opened in Preview only with PEEK_OPEN=1
// While Kun has stopped the office agents (latch.mjs), every command except state/shot/peek exits 4
// before touching Chrome, and every call carries the generation it started under, so a command that
// was already running when Kun pressed stop is refused by the broker at its next step.
import fs from 'fs'; import os from 'os'; import path from 'path'; import { execFileSync } from 'child_process';
import { readLatch, vdHome } from './latch.mjs';
import { bringWindow, handoffNotice, handoffWindow, notifyCommand } from './handoff.mjs';
const home = os.homedir(), vdDir = vdHome();
const brokerURL = `http://127.0.0.1:${process.env.OFFICE_VD_PORT || 9339}/`;
// All CDP goes through broker.mjs (one approved connection), so agents never trigger Chrome's prompt.
// Mirror Chrome's endpoint for the broker (it runs under launchd and cannot read Chrome's folder).
try { const e = fs.readFileSync(`${home}/Library/Application Support/Google/Chrome/DevToolsActivePort`, 'utf8'), m = path.join(vdDir, 'devtools-endpoint');
  if (!fs.existsSync(m) || fs.readFileSync(m, 'utf8') !== e) fs.writeFileSync(m, e); } catch {}
const vd = () => JSON.parse(fs.readFileSync(path.join(vdDir, 'state.json'), 'utf8'));
const [cmd, a, b] = process.argv.slice(2);
const startLatch = readLatch(vdDir);
if (startLatch.state !== 'running' && !['state', 'shot', 'peek'].includes(cmd)) {
  console.error('office agents are stopped on this Mac (Kun pressed stop); not touching Chrome');
  process.exit(4);
}
const generation = startLatch.state === 'running' ? startLatch.generation : undefined;
const send = async (method, params = {}, sessionId) => {
  const r = await fetch(brokerURL, { method: 'POST', body: JSON.stringify({ method, params, sessionId, generation }) });
  const j = await r.json(); if (!r.ok) throw new Error(j.error); return j;
};
const sleep = ms => new Promise(r => setTimeout(r, ms));
setTimeout(() => { console.error('timeout'); process.exit(3); }, 45000);
const onVD = () => { const d = vd(); return { left: d.x + 40, top: d.y + 40, width: d.width - 80, height: d.height - 80 }; };
const winOf = async t => (await send('Browser.getWindowForTarget', { targetId: t })).windowId;
(async () => {
  try {
    if (cmd === 'open') {
      const r = await send('Target.createTarget', { url: a, newWindow: true, background: true, ...onVD() });
      await send('Browser.setWindowBounds', { windowId: await winOf(r.targetId), bounds: { ...onVD(), windowState: 'normal' } });
      console.log(r.targetId);
    } else if (cmd === 'shot') {
      const { sessionId } = await send('Target.attachToTarget', { targetId: a, flatten: true });
      const s = await send('Page.captureScreenshot', { format: 'png' }, sessionId);
      fs.writeFileSync(b, Buffer.from(s.data, 'base64')); console.log(b);
    } else if (cmd === 'look') {
      // Main screen (the one with the menu bar) and its visible frame, converted to top-left coordinates.
      const main = JSON.parse(execFileSync('osascript', ['-l', 'JavaScript', '-e', 'ObjC.import("AppKit");const s=$.NSScreen.screens.objectAtIndex(0),f=s.frame,v=s.visibleFrame;JSON.stringify({w:f.size.width,h:f.size.height,visible:{x:v.origin.x,y:f.size.height-v.origin.y-v.size.height,w:v.size.width,h:v.size.height}})']).toString());
      const r = await handoffWindow(send, a, main);
      if (!r.verified) console.error(`window did not land where asked: wanted ${JSON.stringify(r.bounds)}, Chrome reports ${JSON.stringify(r.actual)}`);
      const notifier = [process.env.OFFICE_VD_NOTIFIER, '/opt/homebrew/bin/terminal-notifier', '/usr/local/bin/terminal-notifier'].find(f => f && fs.existsSync(f));
      const [bin, args] = notifyCommand(handoffNotice(b), { notifier, node: process.execPath, script: new URL(import.meta.url).pathname, targetId: a,
        env: { OFFICE_VD_HOME: process.env.OFFICE_VD_HOME, OFFICE_VD_PORT: process.env.OFFICE_VD_PORT } });
      if (process.env.OFFICE_VD_NOTIFY_DRY === '1') console.log(JSON.stringify({ bounds: r.bounds, verified: r.verified, notify: [bin, ...args] }));
      else execFileSync(bin, args);
      console.log('shown to Kun');
    } else if (cmd === 'bring') {
      await bringWindow(send, a); console.log('brought');
    } else if (cmd === 'back') {
      await send('Browser.setWindowBounds', { windowId: await winOf(a), bounds: onVD() });
      // Make Kun's frontmost on-screen Chrome window the main window again, without activating Chrome,
      // so his keystrokes never land in the hidden window.
      const port = fs.readFileSync(`${home}/Library/Application Support/Google/Chrome/DevToolsActivePort`, 'utf8').split('\n')[0];
      const pid = execFileSync('lsof', ['-ti', `tcp:${port}`, '-sTCP:LISTEN']).toString().trim().split('\n')[0];
      execFileSync('osascript', ['-e', `tell application "System Events" to tell (first process whose unix id is ${pid})
  repeat with w in windows
    if (item 1 of (position of w as list)) < ${vd().x} then
      set value of attribute "AXMain" of w to true
      exit repeat
    end if
  end repeat
end tell`], { timeout: 15000 });
      console.log('back');
    } else if (cmd === 'peek') {
      // Kun wants to look: screenshot every page on the virtual display and open them in Preview.
      const d = vd(), dir = path.join(vdDir, 'peek'), files = [];
      fs.rmSync(dir, { recursive: true, force: true }); fs.mkdirSync(dir);
      for (const t of (await send('Target.getTargets')).targetInfos.filter(t => t.type === 'page')) {
        const { bounds } = await send('Browser.getWindowForTarget', { targetId: t.targetId });
        if (bounds.left < d.x || bounds.top < d.y) continue;
        const { sessionId } = await send('Target.attachToTarget', { targetId: t.targetId, flatten: true });
        const s = await send('Page.captureScreenshot', { format: 'png' }, sessionId).catch(() => null);
        await send('Target.detachFromTarget', { sessionId });
        if (!s) continue;
        const f = `${dir}/${files.length + 1}-${t.title.replace(/[^\p{L}\p{N}]+/gu, '_').slice(0, 40)}.png`;
        fs.writeFileSync(f, Buffer.from(s.data, 'base64')); files.push(f);
      }
      if (files.length && process.env.PEEK_OPEN === '1') execFileSync('open', ['-a', 'Preview', ...files]);
      console.log(files.length ? files.join('\n') : 'no agent pages on the virtual display');
    } else if (cmd === 'close') {
      await send('Target.closeTarget', { targetId: a }); console.log('closed');
    } else if (cmd === 'state') {
      const { targetInfos } = await send('Target.getTargets');
      const out = {};
      for (const t of targetInfos.filter(t => t.type === 'page' && !t.url.startsWith('chrome-extension'))) {
        const { windowId, bounds } = await send('Browser.getWindowForTarget', { targetId: t.targetId });
        const { sessionId } = await send('Target.attachToTarget', { targetId: t.targetId, flatten: true });
        const v = await send('Runtime.evaluate', { expression: 'document.visibilityState' }, sessionId).catch(() => ({ result: { value: '?' } }));
        await send('Target.detachFromTarget', { sessionId });
        const k = `win ${windowId} @${bounds.left},${bounds.top} ${bounds.width}x${bounds.height} ${bounds.windowState}`;
        (out[k] ??= { tabs: 0, visible: [] }).tabs++;
        if (v.result.value === 'visible') out[k].visible.push(t.title.slice(0, 50));
      }
      console.log(JSON.stringify(out, null, 1));
    } else { console.error('usage: open|shot|look|bring|back|peek|close|state'); process.exit(1); }
    process.exit(0);
  } catch (e) { console.error(e.message); process.exit(1); }
})();
