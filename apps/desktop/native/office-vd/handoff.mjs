// handoff.mjs: when an agent needs Kun to act, bring that one window to his main screen WITHOUT taking
// anything from him. It only changes the window's position (Browser.setWindowBounds); it never activates
// Chrome or the page, never moves the pointer and never sends input, so his typing keeps going where it
// was. The window lands in the bottom-right corner of his main screen, behind whatever he has in front.
// A notification tells him; clicking it runs `vd.mjs bring`, the only step that activates the window.
// Limit: macOS cannot raise a window above his others without activating its app, which would steal focus.
// So until he clicks (or switches with Cmd-Tab / the Dock) the window may be covered by his current one.

// main is his main screen {w, h}, optionally with visible: {x, y, w, h}, its visible frame (top-left
// origin, without the menu bar and Dock), so the window never lands under the Dock. Without a visible
// frame, a 32 pt band at the top is kept free for the menu bar (as yomado's AppWorkspacePolicy does).
export function usableArea(main) {
  const v = main.visible;
  if (v && v.w > 0 && v.h > 0) return v;
  const bar = Math.min(32, main.h - 1);
  return { x: 0, y: bar, w: main.w, h: main.h - bar };
}

export function handoffBounds(main, { maxW = 1280, maxH = 820, margin = 100, inset = 16 } = {}) {
  const a = usableArea(main);
  const width = Math.min(maxW, a.w - margin), height = Math.min(maxH, a.h - margin);
  return { left: a.x + a.w - width - inset, top: a.y + a.h - height - inset, width, height, windowState: 'normal' };
}

// A successful setWindowBounds is not proof (after yomado's WindowRecoveryPolicy): Chrome may keep the
// window fullscreen or clamp it. The frame read back must be where we asked, within 2 pt.
export function landed(actual, want, epsilon = 2) {
  if (!actual) return false;
  return ['left', 'top', 'width', 'height'].every(k => Math.abs(actual[k] - want[k]) <= epsilon)
    && (actual.windowState ?? 'normal') === 'normal';
}

// send(method, params) goes through the broker, so the stop latch applies: while stopped this is refused.
export async function handoffWindow(send, targetId, main) {
  const { windowId } = await send('Browser.getWindowForTarget', { targetId });
  const bounds = handoffBounds(main);
  await send('Browser.setWindowBounds', { windowId, bounds });
  let actual = null;
  try { actual = (await send('Browser.getWindowBounds', { windowId })).bounds; } catch {}
  return { windowId, bounds, actual, verified: landed(actual, bounds) };
}

// Only ever called from Kun's click on the notification (or by him on purpose).
export async function bringWindow(send, targetId) {
  await send('Target.activateTarget', { targetId });
}

export function handoffNotice(message) {
  return {
    title: '员工需要你点一下',
    body: message || '请在这页操作',
    hint: '窗口已放到你主屏右下角，没抢焦点；点这条通知切过去',
    fallbackHint: '窗口已放到你主屏右下角，没抢焦点；用 Cmd-Tab 或 Dock 切到 Chrome',
  };
}

const sh = s => `'${String(s).replace(/'/g, `'\\''`)}'`;

// The notification command. With terminal-notifier, a click runs `vd.mjs bring <targetId>` (still through
// the broker and the latch). Without it, osascript's notification can't run anything on click (clicking it
// opens Script Editor), so the text tells him to switch himself.
export function notifyCommand(notice, { notifier, node, script, targetId, env = {} }) {
  if (notifier) {
    const vars = Object.entries(env).filter(([, v]) => v).map(([k, v]) => `${k}=${sh(v)} `).join('');
    return [notifier, ['-title', notice.title, '-message', `${notice.body}。${notice.hint}`, '-sound', 'Glass',
      '-group', `office-vd-${targetId}`, '-execute', `${vars}${sh(node)} ${sh(script)} bring ${sh(targetId)}`]];
  }
  return ['osascript', ['-e', `display notification ${JSON.stringify(`${notice.body}。${notice.fallbackHint}`)} with title ${JSON.stringify(notice.title)} sound name "Glass"`]];
}
