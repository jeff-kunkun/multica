// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, WebContents } from "electron";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

type Handler = (...args: unknown[]) => void;
type IpcHandler = (...args: unknown[]) => unknown;

const ctx = vi.hoisted(() => ({
  handlers: new Map<string, Handler[]>(),
  ipcHandlers: new Map<string, IpcHandler>(),
  ipcHandle: vi.fn(),
  checkForUpdates: vi.fn(async () => ({
    updateInfo: { version: "0.3.18" },
    isUpdateAvailable: false,
  })),
  downloadUpdate: vi.fn(),
  quitAndInstall: vi.fn(),
  getVersion: vi.fn(() => "0.3.17"),
  userDataPath: "",
}));

vi.mock("electron-updater", () => {
  const autoUpdater = {
    autoDownload: false,
    autoInstallOnAppQuit: false,
    channel: undefined as string | undefined,
    allowDowngrade: false,
    on: vi.fn((event: string, handler: Handler) => {
      const handlers = ctx.handlers.get(event) ?? [];
      handlers.push(handler);
      ctx.handlers.set(event, handlers);
      return autoUpdater;
    }),
    checkForUpdates: ctx.checkForUpdates,
    downloadUpdate: ctx.downloadUpdate,
    quitAndInstall: ctx.quitAndInstall,
  };
  return { autoUpdater };
});

vi.mock("electron", () => ({
  app: {
    getVersion: ctx.getVersion,
    getPath: vi.fn(() => ctx.userDataPath),
  },
  BrowserWindow: class BrowserWindow {},
  ipcMain: {
    handle: ctx.ipcHandle,
  },
}));

import { autoUpdater } from "electron-updater";
import {
  applyReleaseChannel,
  feedNameForReleaseChannel,
  setupAutoUpdater,
} from "./updater";
import { updaterPreferencesPath } from "./updater-preferences";

function updaterWithChannelSideEffect() {
  let channel: string | null = null;
  return {
    allowDowngrade: false,
    allowPrerelease: false,
    get channel() {
      return channel;
    },
    set channel(value: string | null) {
      channel = value;
      // AppUpdater.channel does this. Tests below must still observe the
      // value applyReleaseChannel assigns afterwards.
      this.allowDowngrade = true;
    },
  };
}

describe("release channel feed", () => {
  it("keeps the established stable names and uses beta for the test line", () => {
    const stable = {
      "darwin:arm64": null,
      "darwin:x64": "latest-x64",
      "win32:x64": null,
      "win32:arm64": "latest-arm64",
      "linux:x64": null,
      "linux:arm64": null,
    } as const;
    const test = {
      "darwin:arm64": "beta",
      "darwin:x64": "beta-x64",
      "win32:x64": "beta",
      "win32:arm64": "beta-arm64",
      "linux:x64": "beta",
      "linux:arm64": "beta",
    } as const;

    for (const [key, channel] of Object.entries(stable)) {
      const [platform, arch] = key.split(":") as [NodeJS.Platform, string];
      expect(feedNameForReleaseChannel("stable", platform, arch)).toBe(channel);
    }
    for (const [key, channel] of Object.entries(test)) {
      const [platform, arch] = key.split(":") as [NodeJS.Platform, string];
      expect(feedNameForReleaseChannel("test", platform, arch)).toBe(channel);
    }
  });

  it("turns allowDowngrade back off after the channel setter enables it", () => {
    const updater = updaterWithChannelSideEffect();

    applyReleaseChannel(updater, "stable", "0.5.4", "darwin", "x64");

    expect(updater.channel).toBe("latest-x64");
    expect(updater.allowDowngrade).toBe(false);
    expect(updater.allowPrerelease).toBe(false);
  });

  it("allows a downgrade only while a test build is pointed at stable", () => {
    const updater = updaterWithChannelSideEffect();

    applyReleaseChannel(updater, "stable", "0.5.5-test.3", "darwin", "arm64");
    expect(updater.channel).toBeNull();
    expect(updater.allowDowngrade).toBe(true);

    applyReleaseChannel(updater, "test", "0.5.5-test.3", "win32", "arm64");
    expect(updater.channel).toBe("beta-arm64");
    expect(updater.allowDowngrade).toBe(false);
    expect(updater.allowPrerelease).toBe(true);

    applyReleaseChannel(updater, "stable", "0.5.5-test.3", "darwin", "arm64");
    expect(updater.channel).toBe("latest");
    expect(updater.allowDowngrade).toBe(true);
    expect(updater.allowPrerelease).toBe(false);
  });
});

function emitUpdater(event: string, ...args: unknown[]) {
  for (const handler of ctx.handlers.get(event) ?? []) {
    handler(...args);
  }
}

async function invokeIpc(channel: string, ...args: unknown[]) {
  const handler = ctx.ipcHandlers.get(channel);
  if (!handler) throw new Error(`Missing IPC handler: ${channel}`);
  return handler({}, ...args);
}

function makeWindow() {
  const send = vi.fn();
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => false,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

function makeDestroyedWindow() {
  return {
    isDestroyed: () => true,
    get webContents(): WebContents {
      throw new TypeError("Object has been destroyed");
    },
  } as unknown as BrowserWindow;
}

function makeWindowWithDestroyedWebContents() {
  const send = vi.fn(() => {
    throw new TypeError("Object has been destroyed");
  });
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => true,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

function makeWindowWithThrowingSend(error: Error) {
  const send = vi.fn(() => {
    throw error;
  });
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => false,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

describe("setupAutoUpdater", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    ctx.userDataPath = mkdtempSync(join(tmpdir(), "multica-updater-test-"));
    ctx.handlers.clear();
    ctx.ipcHandlers.clear();
    ctx.ipcHandle.mockClear();
    ctx.ipcHandle.mockImplementation((channel: string, handler: IpcHandler) => {
      ctx.ipcHandlers.set(channel, handler);
    });
    ctx.checkForUpdates.mockClear();
    ctx.downloadUpdate.mockClear();
    ctx.quitAndInstall.mockClear();
    ctx.getVersion.mockReset();
    ctx.getVersion.mockReturnValue("0.3.17");
    autoUpdater.channel = null;
    autoUpdater.allowDowngrade = false;
    autoUpdater.allowPrerelease = false;
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    rmSync(ctx.userDataPath, { recursive: true, force: true });
  });

  it("enables automatic background updates by default", async () => {
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:get-preferences")).resolves.toEqual({
      automaticUpdates: true,
      releaseChannel: "stable",
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("skips startup and periodic checks when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setupAutoUpdater(() => null);

    // Let the async preference load settle before advancing timers; otherwise
    // the in-flight readFile can resolve after afterEach() removes the temp
    // dir, default back to enabled=true, and fire a background check into the
    // next test's freshly-cleared mock (flake on slow CI).
    await invokeIpc("updater:get-preferences");

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);

    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("persists the automatic update preference and stops future background checks", async () => {
    setupAutoUpdater(() => null);

    await expect(
      invokeIpc("updater:set-automatic-updates", false),
    ).resolves.toEqual({ automaticUpdates: false, releaseChannel: "stable" });
    expect(
      JSON.parse(
        readFileSync(updaterPreferencesPath(ctx.userDataPath), "utf-8"),
      ),
    ).toEqual({ automaticUpdates: false, releaseChannel: "stable" });

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("still allows an explicit manual check when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:check")).resolves.toMatchObject({
      ok: true,
    });

    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("forwards update progress to a live renderer", () => {
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);

    emitUpdater("download-progress", { percent: 42 });

    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("skips update progress when the BrowserWindow has already been destroyed", () => {
    setupAutoUpdater(() => makeDestroyedWindow());

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
  });

  it("skips update progress when the BrowserWindow webContents has already been destroyed", () => {
    const { win, send } = makeWindowWithDestroyedWebContents();
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).not.toHaveBeenCalled();
  });

  it("skips update progress when webContents.send loses a destroy race", () => {
    const { win, send } = makeWindowWithThrowingSend(
      new TypeError("Object has been destroyed"),
    );
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("uses the saved release channel for the feed", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false, releaseChannel: "test" }),
    );
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:get-preferences")).resolves.toEqual({
      automaticUpdates: false,
      releaseChannel: "test",
    });
    expect(autoUpdater.channel).toBe(
      feedNameForReleaseChannel("test", process.platform, process.arch),
    );
    expect(autoUpdater.allowPrerelease).toBe(true);
    expect(autoUpdater.allowDowngrade).toBe(false);
  });

  it("rechecks immediately when the release channel changes", async () => {
    setupAutoUpdater(() => null);
    await invokeIpc("updater:get-preferences");
    ctx.checkForUpdates.mockClear();

    await expect(invokeIpc("updater:set-release-channel", "test")).resolves.toEqual({
      automaticUpdates: true,
      releaseChannel: "test",
    });

    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
    expect(autoUpdater.channel).toBe(
      feedNameForReleaseChannel("test", process.platform, process.arch),
    );
  });

  it("allows a downgrade when a test build switches back to stable", async () => {
    ctx.getVersion.mockReturnValue("0.5.5-test.3");
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: true, releaseChannel: "test" }),
    );
    setupAutoUpdater(() => null);
    await invokeIpc("updater:get-preferences");
    expect(autoUpdater.allowDowngrade).toBe(false);
    ctx.checkForUpdates.mockClear();

    await invokeIpc("updater:set-release-channel", "stable");

    expect(autoUpdater.allowDowngrade).toBe(true);
    expect(autoUpdater.allowPrerelease).toBe(false);
    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("rethrows non-destroy errors from webContents.send", () => {
    const { win } = makeWindowWithThrowingSend(new Error("boom"));
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).toThrow(
      "boom",
    );
  });
});
