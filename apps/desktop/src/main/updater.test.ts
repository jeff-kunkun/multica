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
  openPath: vi.fn(async () => ""),
  showItemInFolder: vi.fn(),
  log: {
    error: vi.fn(),
    warn: vi.fn(),
    info: vi.fn(),
    debug: vi.fn(),
    transports: { file: { getFile: () => ({ path: "/logs/main.log" }) } },
  },
}));

vi.mock("electron-updater", () => {
  const autoUpdater = {
    autoDownload: false,
    autoInstallOnAppQuit: false,
    logger: null as unknown,
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
    isPackaged: true,
  },
  BrowserWindow: class BrowserWindow {},
  ipcMain: {
    handle: ctx.ipcHandle,
  },
  shell: {
    openPath: ctx.openPath,
    showItemInFolder: ctx.showItemInFolder,
  },
}));

vi.mock("electron-log/main", () => ({ default: ctx.log }));

import { autoUpdater } from "electron-updater";
import log from "electron-log/main";
import {
  configureMacX64UpdateChannel,
  resolveCapabilities,
  setupAutoUpdater,
  type SetupAutoUpdaterOptions,
} from "./updater";
import { updaterPreferencesPath } from "./updater-preferences";
import type { UpdaterCapabilities } from "../shared/updater-types";

const SUPPORTED: UpdaterCapabilities = {
  autoUpdateSupported: true,
  assistedInstallSupported: false,
  blocker: null,
  releasePageUrl: "https://github.com/jeff-kunkun/multica/releases/latest",
  logPath: "/logs/main.log",
};

/** A default ad-hoc macOS build: no in-place install, but it fetches the .dmg. */
const MAC_UNSIGNED: UpdaterCapabilities = {
  ...SUPPORTED,
  autoUpdateSupported: false,
  assistedInstallSupported: true,
  blocker: "mac-unsigned",
};

// Every setup in this file resolves capabilities synchronously-ish via a stub
// so the platform / codesign probe never touches the host machine.
function setup(
  getMainWindow: () => BrowserWindow | null,
  capabilities: UpdaterCapabilities = SUPPORTED,
  fetchInstaller?: SetupAutoUpdaterOptions["fetchInstaller"],
) {
  setupAutoUpdater(getMainWindow, {
    resolveCapabilities: async () => capabilities,
    fetchInstaller,
  });
}

describe("macOS x64 update channel", () => {
  it("does not touch established architecture paths", () => {
    for (const [platform, arch] of [
      ["darwin", "arm64"],
      ["win32", "x64"],
      ["win32", "arm64"],
      ["linux", "arm64"],
    ] as const) {
      const updater = { channel: null, allowDowngrade: true };

      configureMacX64UpdateChannel(updater, platform, arch);

      expect(updater).toEqual({ channel: null, allowDowngrade: true });
    }
  });

  it("does not enable downgrades when selecting an architecture feed", () => {
    const updater = { channel: null, allowDowngrade: true };

    configureMacX64UpdateChannel(updater, "darwin", "x64");

    expect(updater).toEqual({
      channel: "latest-x64",
      allowDowngrade: false,
    });
  });
});

// The check pipeline chains several already-resolved promises (preferences →
// capabilities → checkForUpdates → record). Fake timers flush only a few
// microtask turns per advance, so drain the queue explicitly before asserting.
async function flushPromises(turns = 10) {
  for (let i = 0; i < turns; i += 1) await Promise.resolve();
}

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
    ctx.getVersion.mockClear();
    ctx.openPath.mockClear();
    ctx.showItemInFolder.mockClear();
    ctx.log.error.mockClear();
    ctx.log.warn.mockClear();
    (autoUpdater as unknown as { logger: unknown }).logger = null;
    (autoUpdater as unknown as { autoDownload: boolean }).autoDownload = false;
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    rmSync(ctx.userDataPath, { recursive: true, force: true });
  });

  it("enables automatic background updates by default", async () => {
    setup(() => null);

    await expect(invokeIpc("updater:get-preferences")).resolves.toEqual({
      automaticUpdates: true,
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("skips startup and periodic checks when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setup(() => null);

    // Let the async preference load settle before advancing timers; otherwise
    // the in-flight readFile can resolve after afterEach() removes the temp
    // dir, default back to enabled=true, and fire a background check into the
    // next test's freshly-cleared mock (flake on slow CI).
    await invokeIpc("updater:get-preferences");

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);

    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("persists the automatic update preference and stops future background checks", async () => {
    setup(() => null);

    await expect(
      invokeIpc("updater:set-automatic-updates", false),
    ).resolves.toEqual({ automaticUpdates: false });
    expect(
      JSON.parse(
        readFileSync(updaterPreferencesPath(ctx.userDataPath), "utf-8"),
      ),
    ).toEqual({ automaticUpdates: false });

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("still allows an explicit manual check when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setup(() => null);

    await expect(invokeIpc("updater:check")).resolves.toMatchObject({
      ok: true,
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

  it("routes electron-updater diagnostics through electron-log", () => {
    setup(() => null);

    expect((autoUpdater as unknown as { logger: unknown }).logger).toBe(log);
  });

  it("forwards updater errors to a live renderer and logs them", () => {
    const { win, send } = makeWindow();
    setup(() => win);

    emitUpdater("error", new Error("net::ERR_INTERNET_DISCONNECTED"));

    expect(send).toHaveBeenCalledWith("updater:error", {
      message: "net::ERR_INTERNET_DISCONNECTED",
    });
    expect(ctx.log.error).toHaveBeenCalledWith(
      "[updater] error:",
      expect.any(Error),
    );
  });

  it("does not throw when an updater error arrives after the window is gone", () => {
    setup(() => makeDestroyedWindow());

    expect(() => emitUpdater("error", new Error("late"))).not.toThrow();
  });

  it("forwards update-available and update-downloaded to a live renderer", () => {
    const { win, send } = makeWindow();
    setup(() => win);

    emitUpdater("update-available", { version: "0.3.18", releaseNotes: "notes" });
    emitUpdater("update-downloaded", { version: "0.3.18", releaseNotes: "notes" });

    expect(send).toHaveBeenCalledWith("updater:update-available", {
      version: "0.3.18",
      releaseNotes: "notes",
    });
    expect(send).toHaveBeenCalledWith("updater:update-downloaded", {
      version: "0.3.18",
      releaseNotes: "notes",
    });
  });

  it("records the startup check outcome and pushes it to the renderer", async () => {
    const { win, send } = makeWindow();
    setup(() => win);
    // Preferences come off disk (real I/O, not a faked timer); wait for that
    // read before firing the startup timer so the check runs deterministically.
    await invokeIpc("updater:get-preferences");

    await vi.advanceTimersByTimeAsync(5_000);
    await flushPromises();

    const record = await invokeIpc("updater:get-last-check");
    expect(record).toMatchObject({
      trigger: "startup",
      ok: true,
      available: false,
      latestVersion: "0.3.18",
    });
    expect(send).toHaveBeenCalledWith("updater:checking", { trigger: "startup" });
    expect(send).toHaveBeenCalledWith("updater:check-result", record);
  });

  it("records a failed periodic check instead of staying silent", async () => {
    const { win, send } = makeWindow();
    setup(() => win);
    await invokeIpc("updater:get-preferences");
    await vi.advanceTimersByTimeAsync(5_000);
    await flushPromises();
    ctx.checkForUpdates.mockRejectedValueOnce(new Error("HttpError: 404"));

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
    await flushPromises();

    await expect(invokeIpc("updater:get-last-check")).resolves.toMatchObject({
      trigger: "periodic",
      ok: false,
      error: "HttpError: 404",
    });
    expect(send).toHaveBeenCalledWith(
      "updater:check-result",
      expect.objectContaining({ ok: false, error: "HttpError: 404" }),
    );
    expect(ctx.log.error).toHaveBeenCalledWith(
      "[updater] periodic check failed:",
      expect.any(Error),
    );
  });

  it("returns the manual check result and records it with the manual trigger", async () => {
    setup(() => null);
    ctx.checkForUpdates.mockResolvedValueOnce({
      updateInfo: { version: "0.4.0" },
      isUpdateAvailable: true,
    });

    await expect(invokeIpc("updater:check")).resolves.toEqual({
      ok: true,
      currentVersion: "0.3.17",
      latestVersion: "0.4.0",
      available: true,
    });
    await expect(invokeIpc("updater:get-last-check")).resolves.toMatchObject({
      trigger: "manual",
      ok: true,
      available: true,
    });
  });

  it("exposes capabilities and turns off autoDownload on an unsigned macOS build", async () => {
    setup(() => null, MAC_UNSIGNED);

    await expect(invokeIpc("updater:get-capabilities")).resolves.toEqual(
      MAC_UNSIGNED,
    );
    expect((autoUpdater as unknown as { autoDownload: boolean }).autoDownload).toBe(
      false,
    );
    expect(ctx.log.warn).toHaveBeenCalledWith(
      expect.stringContaining("mac-unsigned"),
    );
  });

  it("keeps autoDownload on when the build can install updates", async () => {
    setup(() => null, SUPPORTED);

    await invokeIpc("updater:get-capabilities");

    expect((autoUpdater as unknown as { autoDownload: boolean }).autoDownload).toBe(
      true,
    );
  });

  it("runs the manual download through electron-updater and surfaces failures", async () => {
    setup(() => null, SUPPORTED);
    ctx.downloadUpdate.mockResolvedValueOnce(undefined);

    await expect(invokeIpc("updater:download")).resolves.toBeUndefined();
    expect(ctx.downloadUpdate).toHaveBeenCalledTimes(1);

    ctx.downloadUpdate.mockRejectedValueOnce(new Error("disk full"));
    await expect(invokeIpc("updater:download")).rejects.toThrow("disk full");
  });

  // --- Assisted install: the default macOS build, no certificate anywhere ---
  // These cover the path a `CSC_LINK`-less release takes: electron-updater
  // cannot install in place, so the app fetches the .dmg itself and asks for
  // the drag into Applications.
  describe("assisted install on an unsigned macOS build", () => {
    const installer = {
      path: "/Users/x/Library/Application Support/Multica/installers/multica-desktop-0.4.0-mac-arm64.dmg",
      fileName: "multica-desktop-0.4.0-mac-arm64.dmg",
      bytes: 220_000_000,
    };

    type FetchInstaller = NonNullable<SetupAutoUpdaterOptions["fetchInstaller"]>;

    function setupAssisted(
      getMainWindow: () => BrowserWindow | null,
      fetchInstaller: ReturnType<typeof vi.fn<FetchInstaller>> = vi.fn<FetchInstaller>(
        async () => installer,
      ),
    ) {
      setup(getMainWindow, MAC_UNSIGNED, fetchInstaller);
      return fetchInstaller;
    }

    it("downloads the installer itself as soon as an update is offered", async () => {
      const { win, send } = makeWindow();
      const fetchInstaller = setupAssisted(() => win);

      emitUpdater("update-available", { version: "0.4.0" });
      await flushPromises();

      expect(fetchInstaller).toHaveBeenCalledWith("0.4.0", expect.any(Function));
      expect(send).toHaveBeenCalledWith("updater:download-progress", { percent: 0 });
      expect(send).toHaveBeenCalledWith("updater:installer-ready", {
        version: "0.4.0",
        fileName: installer.fileName,
        path: installer.path,
      });
      // The renderer must not be told to restart: nothing was staged.
      expect(send).not.toHaveBeenCalledWith(
        "updater:update-downloaded",
        expect.anything(),
      );
    });

    it("reports the installer download's own progress", async () => {
      const { win, send } = makeWindow();
      setupAssisted(
        () => win,
        vi.fn<FetchInstaller>(async (_version, onProgress) => {
          onProgress(42);
          return installer;
        }),
      );

      emitUpdater("update-available", { version: "0.4.0" });
      await flushPromises();

      expect(send).toHaveBeenCalledWith("updater:download-progress", { percent: 42 });
    });

    it("never stages a package Squirrel would refuse", async () => {
      setupAssisted(() => null);

      await invokeIpc("updater:get-capabilities");

      expect((autoUpdater as unknown as { autoDownload: boolean }).autoDownload).toBe(
        false,
      );
      expect(ctx.downloadUpdate).not.toHaveBeenCalled();
    });

    it("routes the Download button to the installer fetch, once", async () => {
      const fetchInstaller = setupAssisted(() => null);

      emitUpdater("update-available", { version: "0.4.0" });
      await Promise.all([
        invokeIpc("updater:download"),
        invokeIpc("updater:download"),
      ]);

      expect(fetchInstaller).toHaveBeenCalledTimes(1);
      expect(ctx.downloadUpdate).not.toHaveBeenCalled();
    });

    it("falls back to the last check when no update-available event fired", async () => {
      const fetchInstaller = setupAssisted(() => null);
      ctx.checkForUpdates.mockResolvedValueOnce({
        updateInfo: { version: "0.4.0" },
        isUpdateAvailable: true,
      });

      await invokeIpc("updater:check");
      await invokeIpc("updater:download");

      expect(fetchInstaller).toHaveBeenCalledWith("0.4.0", expect.any(Function));
    });

    it("refuses to guess a version nobody has been offered", async () => {
      setupAssisted(() => null);

      await expect(invokeIpc("updater:download")).rejects.toThrow(
        "No update version has been offered yet",
      );
    });

    it("surfaces a failed installer download instead of going quiet", async () => {
      const { win, send } = makeWindow();
      setupAssisted(
        () => win,
        vi.fn<FetchInstaller>(async () => {
          throw new Error("HTTP 404 for .../multica-desktop-0.4.0-mac-arm64.dmg");
        }),
      );

      emitUpdater("update-available", { version: "0.4.0" });
      await flushPromises();

      expect(send).toHaveBeenCalledWith("updater:error", {
        message: expect.stringContaining("HTTP 404"),
      });
      expect(send).not.toHaveBeenCalledWith(
        "updater:installer-ready",
        expect.anything(),
      );
    });

    it("opens and reveals the downloaded installer", async () => {
      setupAssisted(() => null);
      emitUpdater("update-available", { version: "0.4.0" });
      await flushPromises();

      await expect(invokeIpc("updater:get-installer")).resolves.toMatchObject({
        version: "0.4.0",
        path: installer.path,
      });
      await expect(invokeIpc("updater:open-installer")).resolves.toEqual({
        success: true,
      });
      expect(ctx.openPath).toHaveBeenCalledWith(installer.path);
      await expect(invokeIpc("updater:reveal-installer")).resolves.toEqual({
        success: true,
      });
      expect(ctx.showItemInFolder).toHaveBeenCalledWith(installer.path);
    });

    it("reports the reason when opening fails", async () => {
      setupAssisted(() => null);
      emitUpdater("update-available", { version: "0.4.0" });
      await flushPromises();
      ctx.openPath.mockResolvedValueOnce("disk image is corrupt");

      await expect(invokeIpc("updater:open-installer")).resolves.toEqual({
        success: false,
        error: "disk image is corrupt",
      });
    });

    it("has nothing to open before a download has finished", async () => {
      setupAssisted(() => null);

      await expect(invokeIpc("updater:get-installer")).resolves.toBeNull();
      await expect(invokeIpc("updater:open-installer")).resolves.toEqual({
        success: false,
        error: "No installer downloaded",
      });
      await expect(invokeIpc("updater:reveal-installer")).resolves.toEqual({
        success: false,
        error: "No installer downloaded",
      });
      expect(ctx.openPath).not.toHaveBeenCalled();
    });
  });

  it("opens the electron-log file for the updater", async () => {
    setup(() => null);

    await expect(invokeIpc("updater:open-log")).resolves.toEqual({ success: true });
    expect(ctx.openPath).toHaveBeenCalledWith("/logs/main.log");
  });
});

describe("resolveCapabilities", () => {
  const base = { executablePath: "/x", logPath: null, isPackaged: true };

  it("treats non-macOS platforms as able to auto-update", async () => {
    const detectSigning = vi.fn();
    for (const platform of ["win32", "linux"] as const) {
      await expect(
        resolveCapabilities({ ...base, platform, detectSigning }),
      ).resolves.toMatchObject({ autoUpdateSupported: true, blocker: null });
    }
    expect(detectSigning).not.toHaveBeenCalled();
  });

  it("supports auto-update on any stable signing identity", async () => {
    await expect(
      resolveCapabilities({
        ...base,
        platform: "darwin",
        detectSigning: async () => "identity",
      }),
    ).resolves.toMatchObject({
      autoUpdateSupported: true,
      assistedInstallSupported: false,
      blocker: null,
    });
  });

  it.each(["adhoc", "unsigned", "unknown"] as const)(
    "reports mac-unsigned for a %s macOS build",
    async (signing) => {
      await expect(
        resolveCapabilities({
          ...base,
          platform: "darwin",
          detectSigning: async () => signing,
        }),
      ).resolves.toMatchObject({
        autoUpdateSupported: false,
        assistedInstallSupported: true,
        blocker: "mac-unsigned",
        releasePageUrl: "https://github.com/jeff-kunkun/multica/releases/latest",
      });
    },
  );

  it("skips the signing probe in dev where there is nothing to update", async () => {
    const detectSigning = vi.fn();
    await expect(
      resolveCapabilities({
        ...base,
        platform: "darwin",
        isPackaged: false,
        detectSigning,
      }),
    ).resolves.toMatchObject({ autoUpdateSupported: true });
    expect(detectSigning).not.toHaveBeenCalled();
  });
});
