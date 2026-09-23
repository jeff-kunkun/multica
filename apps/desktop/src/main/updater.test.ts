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
  setFeedURL: vi.fn(),
  getVersion: vi.fn(() => "0.3.17"),
  userDataPath: "",
  listReleases: vi.fn(
    async (): Promise<Array<{ tag_name?: string; draft?: boolean }>> => [],
  ),
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
    setFeedURL: ctx.setFeedURL,
    logger: null as unknown,
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
import { setupAutoUpdater } from "./updater";
import { updaterPreferencesPath } from "./updater-preferences";

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

function setup(getWindow: () => BrowserWindow | null = () => null) {
  return setupAutoUpdater(getWindow, {
    platform: "darwin",
    arch: "arm64",
    macSignedUpdates: false,
    listReleases: () => ctx.listReleases(),
  });
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
    ctx.setFeedURL.mockClear();
    ctx.getVersion.mockReset();
    ctx.getVersion.mockReturnValue("0.3.17");
    ctx.listReleases.mockReset();
    ctx.listReleases.mockResolvedValue([]);
    autoUpdater.autoDownload = false;
    autoUpdater.channel = null;
    autoUpdater.allowDowngrade = false;
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    rmSync(ctx.userDataPath, { recursive: true, force: true });
  });

  it("enables automatic background updates by default", async () => {
    setup();

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
    setup();

    // Let the async preference load settle before advancing timers; otherwise
    // the in-flight readFile can resolve after afterEach() removes the temp
    // dir, default back to enabled=true, and fire a background check into the
    // next test's freshly-cleared mock (flake on slow CI).
    await invokeIpc("updater:get-preferences");

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);

    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("persists the automatic update preference and stops future background checks", async () => {
    setup();

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
    setup();

    await expect(invokeIpc("updater:check")).resolves.toMatchObject({
      ok: true,
      installMode: "manual",
    });

    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("forwards update progress to a live renderer", () => {
    const { win, send } = makeWindow();
    setup(() => win);

    emitUpdater("download-progress", { percent: 42 });

    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("skips update progress when the BrowserWindow has already been destroyed", () => {
    setup(() => makeDestroyedWindow());

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
  });

  it("skips update progress when the BrowserWindow webContents has already been destroyed", () => {
    const { win, send } = makeWindowWithDestroyedWebContents();
    setup(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).not.toHaveBeenCalled();
  });

  it("skips update progress when webContents.send loses a destroy race", () => {
    const { win, send } = makeWindowWithThrowingSend(
      new TypeError("Object has been destroyed"),
    );
    setup(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("rethrows non-destroy errors from webContents.send", () => {
    const { win } = makeWindowWithThrowingSend(new Error("boom"));
    setup(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).toThrow(
      "boom",
    );
  });

  it("tells an unsigned Mac build to download the installer instead of installing it", () => {
    const { win, send } = makeWindow();
    setup(() => win);

    emitUpdater("update-available", { version: "0.5.6", releaseNotes: "" });

    expect(autoUpdater.autoDownload).toBe(false);
    expect(send).toHaveBeenCalledWith(
      "updater:state",
      expect.objectContaining({
        phase: "available",
        version: "0.5.6",
        installMode: "manual",
        manualDownloadUrl:
          "https://github.com/jeff-kunkun/multica/releases/download/v0.5.6/multica-desktop-0.5.6-mac-arm64.dmg",
      }),
    );
  });

  it("downloads on Windows and records a failure in the updater log", async () => {
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win, {
      platform: "win32",
      arch: "x64",
      macSignedUpdates: false,
      listReleases: () => ctx.listReleases(),
    });
    await invokeIpc("updater:get-preferences");

    expect(autoUpdater.autoDownload).toBe(true);
    emitUpdater("error", new Error("signature mismatch"));

    expect(send).toHaveBeenCalledWith(
      "updater:state",
      expect.objectContaining({
        phase: "error",
        error: "signature mismatch",
        errorCode: "check_failed",
      }),
    );
    const log = readFileSync(join(ctx.userDataPath, "logs", "updater.log"), "utf8");
    expect(log).toContain("signature mismatch");
  });

  it("points the test line at the beta feed for the newest test tag", async () => {
    ctx.listReleases.mockResolvedValue([
      { tag_name: "v0.5.5" },
      { tag_name: "v0.5.6-test.2" },
      { tag_name: "v0.5.6-test.1" },
    ]);
    setup();

    await expect(invokeIpc("updater:set-release-channel", "test")).resolves.toEqual({
      automaticUpdates: true,
      releaseChannel: "test",
    });
    await vi.advanceTimersByTimeAsync(0);

    expect(ctx.setFeedURL).toHaveBeenCalledWith({
      provider: "generic",
      url: "https://github.com/jeff-kunkun/multica/releases/download/v0.5.6-test.2",
    });
    expect(autoUpdater.channel).toBe("beta");
    expect(autoUpdater.allowPrerelease).toBe(true);
  });

  it("reports a test line with nothing published instead of checking the stable feed", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false, releaseChannel: "test" }),
    );
    setup();

    await expect(invokeIpc("updater:check")).resolves.toEqual({
      ok: false,
      error: "No test release published",
      errorCode: "no_test_release",
    });
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
    expect(ctx.setFeedURL).not.toHaveBeenCalled();
  });

  it("allows a downgrade when a test build switches back to stable", async () => {
    ctx.getVersion.mockReturnValue("0.5.6-test.3");
    setupAutoUpdater(() => null, {
      platform: "darwin",
      arch: "arm64",
      macSignedUpdates: false,
      listReleases: () => ctx.listReleases(),
    });

    await invokeIpc("updater:check");

    expect(autoUpdater.channel).toBe("latest");
    expect(autoUpdater.allowDowngrade).toBe(true);
    expect(autoUpdater.allowPrerelease).toBe(false);
    expect(ctx.setFeedURL).toHaveBeenCalledWith({
      provider: "github",
      owner: "jeff-kunkun",
      repo: "multica",
    });
  });
});
