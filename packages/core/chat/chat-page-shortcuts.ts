import {
  createShortcutChord,
  getShortcutPlatform,
  shortcutMatchesEvent,
  type ShortcutChord,
  type ShortcutPlatform,
  type ShortcutRuntime,
} from "../shortcuts";

/**
 * New chat on the chat page.
 *
 * Desktop receives bare Mod+N (the shell does not bind it). A browser page
 * cannot own Mod+N (new window) or Mod+Shift+N (private window), so web uses
 * Mod+Alt+N, which those browsers do not reserve. The "+" tooltip shows
 * whichever chord this runtime actually listens for.
 */
export function newChatShortcut(runtime: ShortcutRuntime): ShortcutChord {
  if (runtime === "desktop") return createShortcutChord("N", { primary: true });
  return createShortcutChord("N", { primary: true, alt: true });
}

/** Open the project quick switcher. Mod+Alt+P is free on web and desktop. */
export function projectSwitchShortcut(): ShortcutChord {
  return createShortcutChord("P", { primary: true, alt: true });
}

export type ChatPageShortcutAction = "new-chat" | "switch-project";

/**
 * Page-level chord, or null when the key should keep its ordinary meaning.
 * An open popup owns the keyboard (agent picker, switcher, menus). A text
 * field that is not the composer does too — search and rename must not be
 * hijacked. The composer itself is contenteditable and still starts a chat.
 */
export function chatPageShortcutAction(
  event: KeyboardEvent,
  runtime: ShortcutRuntime,
  gates: { inForeignEditable: boolean; inPortal: boolean },
  platform: ShortcutPlatform = getShortcutPlatform(),
): ChatPageShortcutAction | null {
  if (gates.inPortal || gates.inForeignEditable) return null;
  if (shortcutMatchesEvent(newChatShortcut(runtime), event, platform)) return "new-chat";
  if (shortcutMatchesEvent(projectSwitchShortcut(), event, platform)) return "switch-project";
  return null;
}
