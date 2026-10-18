// handoff.mjs: when an agent needs Kun to act, bring that one window to his main screen WITHOUT taking
// anything from him. It only changes the window's position (Browser.setWindowBounds); it never activates
// Chrome or the page, never moves the pointer and never sends input, so his typing keeps going where it
// was. The window lands in the bottom-right corner of his main screen, behind whatever he has in front.
// A notification tells him; clicking it runs `vd.mjs bring`, the only step that activates the window.
// Limit: macOS cannot raise a window above his others without activating its app, which would steal focus.
// So until he clicks (or switches with Cmd-Tab / the Dock) the window may be covered by his current one.

export function handoffBounds(main, { maxW = 1280, maxH = 820, margin = 100, inset = 16 } = {}) {
  const width = Math.min(maxW, main.w - margin), height = Math.min(maxH, main.h - margin);
  return { left: main.w - width - inset, top: main.h - height - inset, width, height, windowState: 'normal' };
}

// send(method, params) goes through the broker, so the stop latch applies: while stopped this is refused.
export async function handoffWindow(send, targetId, main) {
  const { windowId } = await send('Browser.getWindowForTarget', { targetId });
  const bounds = handoffBounds(main);
  await send('Browser.setWindowBounds', { windowId, bounds });
  return { windowId, bounds };
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
