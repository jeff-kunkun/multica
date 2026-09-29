import { describe, expect, it } from "vitest";
import { chatPageShortcutAction, newChatShortcut, projectSwitchShortcut } from "./chat-page-shortcuts";
import { createShortcutChord } from "../shortcuts";

function keyEvent(
  key: string,
  fields: Partial<Pick<KeyboardEvent, "metaKey" | "ctrlKey" | "altKey" | "shiftKey">> = {},
): KeyboardEvent {
  return {
    key,
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    ...fields,
  } as KeyboardEvent;
}

const open = { inForeignEditable: false, inPortal: false };

describe("newChatShortcut", () => {
  it("uses Mod+N on desktop and Mod+Alt+N on the web", () => {
    expect(newChatShortcut("desktop")).toEqual(createShortcutChord("N", { primary: true }));
    expect(newChatShortcut("web")).toEqual(
      createShortcutChord("N", { primary: true, alt: true }),
    );
  });
});

describe("chatPageShortcutAction", () => {
  it("starts a chat on desktop Mod+N and ignores the browser's Mod+N", () => {
    const macN = keyEvent("n", { metaKey: true });
    expect(chatPageShortcutAction(macN, "desktop", open, "macos")).toBe("new-chat");
    expect(chatPageShortcutAction(macN, "web", open, "macos")).toBeNull();

    const winN = keyEvent("n", { ctrlKey: true });
    expect(chatPageShortcutAction(winN, "desktop", open, "windows")).toBe("new-chat");
    expect(chatPageShortcutAction(winN, "web", open, "windows")).toBeNull();
  });

  it("uses Mod+Alt+N on the web, and that chord does not fire on desktop", () => {
    const mac = keyEvent("n", { metaKey: true, altKey: true });
    expect(chatPageShortcutAction(mac, "web", open, "macos")).toBe("new-chat");
    expect(chatPageShortcutAction(mac, "desktop", open, "macos")).toBeNull();

    const win = keyEvent("n", { ctrlKey: true, altKey: true });
    expect(chatPageShortcutAction(win, "web", open, "windows")).toBe("new-chat");
    expect(chatPageShortcutAction(win, "desktop", open, "windows")).toBeNull();
  });

  it("opens the project switcher on Mod+Alt+P on either runtime", () => {
    expect(projectSwitchShortcut()).toEqual(
      createShortcutChord("P", { primary: true, alt: true }),
    );
    const mac = keyEvent("p", { metaKey: true, altKey: true });
    expect(chatPageShortcutAction(mac, "web", open, "macos")).toBe("switch-project");
    expect(chatPageShortcutAction(mac, "desktop", open, "macos")).toBe("switch-project");
    const win = keyEvent("p", { ctrlKey: true, altKey: true });
    expect(chatPageShortcutAction(win, "web", open, "windows")).toBe("switch-project");
  });

  it("stays quiet inside a popup or a text field that is not the composer", () => {
    const macN = keyEvent("n", { metaKey: true });
    expect(
      chatPageShortcutAction(macN, "desktop", { inForeignEditable: true, inPortal: false }, "macos"),
    ).toBeNull();
    expect(
      chatPageShortcutAction(macN, "desktop", { inForeignEditable: false, inPortal: true }, "macos"),
    ).toBeNull();
  });
});
