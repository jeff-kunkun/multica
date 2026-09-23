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
  checkForUpdates: vi.fn(async (): Promise<{
    updateInfo: { version: string };
    isUpdateAvailable: boolean;
    downloadPromise?: Promise<unknown>;
  }> => ({
    updateInfo: { version: "0.3.18" },
    isUpdateAvailable: false,
  })),
  downloadUpdate: vi.fn(),
  quitAndInstall: vi.fn(),
  getVersion: vi.fn(() => "0.3.17"),
  userDataPath: "",
  isPackaged: false,
  readCodesignOutput: vi.fn(async (_bundlePath: string) => ""),
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
    logger: null as {
      info: (...args: unknown[]) => void;
      warn: (...args: unknown[]) => void;
      error: (...args: unknown[]) => void;
    } | null,
  };
  return { autoUpdater };
});

vi.mock("electron", () => ({
  app: {
    getVersion: ctx.getVersion,
    getPath: vi.fn(() => ctx.userDataPath),
    get isPackaged() {
      return ctx.isPackaged;
    },
  },
  BrowserWindow: class BrowserWindow {},
  ipcMain: {
    handle: ctx.ipcHandle,
  },
}));

vi.mock("./updater-signature", async () => {
  const actual = await vi.importActual<typeof import("./updater-signature")>(
    "./updater-signature",
  );
  return {
    ...actual,
    readCodesignOutput: (bundlePath: string) => ctx.readCodesignOutput(bundlePath),
  };
});

import { autoUpdater } from "electron-updater";
import {
  DESKTOP_RELEASES_PAGE_URL,
  type UpdaterSnapshot,
} from "../shared/updater-types";
import {
  configureMacX64UpdateChannel,
  setupAutoUpdater,
} from "./updater";
import { updaterLogFilePath } from "./updater-logger";
import { updaterPreferencesPath } from "./updater-preferences";

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

function emitUpdater(event: string, ...args: unknown[]) {
  for (const handler of ctx.handlers.get(event) ?? []) {
    handler(...args);
  }
}

async function invokeIpc<T = unknown>(channel: string, ...args: unknown[]): Promise<T> {
  const handler = ctx.ipcHandlers.get(channel);
  if (!handler) throw new Error(`Missing IPC handler: ${channel}`);
  return (await handler({}, ...args)) as T;
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
    ctx.checkForUpdates.mockReset();
    ctx.checkForUpdates.mockImplementation(async () => ({
      updateInfo: { version: "0.3.18" },
      isUpdateAvailable: false,
    }));
    ctx.downloadUpdate.mockClear();
    ctx.quitAndInstall.mockClear();
    ctx.getVersion.mockClear();
    ctx.isPackaged = false;
    ctx.readCodesignOutput.mockReset();
    ctx.readCodesignOutput.mockResolvedValue("");
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

  it("rethrows non-destroy errors from webContents.send", () => {
    const { win } = makeWindowWithThrowingSend(new Error("boom"));
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).toThrow(
      "boom",
    );
  });

  it("forwards updater errors and writes them to the updater log", () => {
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);

    expect(autoUpdater.logger).toMatchObject({
      info: expect.any(Function),
      warn: expect.any(Function),
      error: expect.any(Function),
    });
    autoUpdater.logger?.info("updater-log-probe");
    emitUpdater("error", new Error("Code signature mismatch"));

    expect(send).toHaveBeenCalledWith("updater:error", {
      message: "Code signature mismatch",
    });
    const log = readFileSync(updaterLogFilePath(ctx.userDataPath), "utf8");
    expect(log).toContain("updater-log-probe");
    expect(log).toContain("Code signature mismatch");
  });

  it("forwards a rejected download to the renderer", async () => {
    const downloadPromise = Promise.reject(new Error("download exploded"));
    ctx.checkForUpdates.mockResolvedValue({
      updateInfo: { version: "2.0.0" },
      isUpdateAvailable: true,
      downloadPromise,
    });
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);

    await expect(invokeIpc("updater:check")).resolves.toMatchObject({
      ok: true,
      available: true,
      latestVersion: "2.0.0",
    });
    await downloadPromise.catch(() => undefined);

    expect(send).toHaveBeenCalledWith("updater:error", {
      message: "download exploded",
    });
  });

  it("records startup and hourly check results, including failures", async () => {
    ctx.checkForUpdates
      .mockResolvedValueOnce({
        updateInfo: { version: "0.3.17" },
        isUpdateAvailable: false,
      })
      .mockRejectedValueOnce(new Error("hourly lookup failed"));
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);
    await invokeIpc<UpdaterSnapshot>("updater:get-snapshot");

    await vi.advanceTimersByTimeAsync(5_000);
    expect(send).toHaveBeenCalledWith(
      "updater:check-result",
      expect.objectContaining({
        source: "startup",
        ok: true,
        available: false,
        latestVersion: "0.3.17",
      }),
    );

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
    expect(send).toHaveBeenCalledWith(
      "updater:check-result",
      expect.objectContaining({
        source: "periodic",
        ok: false,
        error: "hourly lookup failed",
      }),
    );

    const snapshot = await invokeIpc<UpdaterSnapshot>("updater:get-snapshot");
    expect(snapshot.lastCheck).toMatchObject({
      source: "periodic",
      ok: false,
      error: "hourly lookup failed",
    });
    expect(snapshot.phase).toBe("error");
  });

  it("reads the running bundle's signature before deciding macOS auto-download", async () => {
    ctx.isPackaged = true;
    ctx.readCodesignOutput.mockResolvedValue(
      "Authority=Developer ID Application: Multica (ABCDE12345)\n",
    );
    setupAutoUpdater(() => null);

    const snapshot = await invokeIpc<UpdaterSnapshot>("updater:get-snapshot");

    if (process.platform === "darwin") {
      expect(ctx.readCodesignOutput).toHaveBeenCalledTimes(1);
      expect(snapshot.manualDownloadRequired).toBe(false);
      expect(autoUpdater.autoDownload).toBe(true);
    } else {
      expect(ctx.readCodesignOutput).not.toHaveBeenCalled();
      expect(snapshot.manualDownloadRequired).toBe(false);
      expect(autoUpdater.autoDownload).toBe(true);
    }
  });

  it("does not auto-download a packaged darwin build without Developer ID", async () => {
    ctx.isPackaged = true;
    ctx.readCodesignOutput.mockResolvedValue("Signature=adhoc\nflags=0x2(adhoc)\n");
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);

    const snapshot = await invokeIpc<UpdaterSnapshot>("updater:get-snapshot");
    emitUpdater("update-available", { version: "9.1.0", releaseNotes: "notes" });

    if (process.platform !== "darwin") {
      expect(snapshot.manualDownloadRequired).toBe(false);
      expect(autoUpdater.autoDownload).toBe(true);
      return;
    }

    expect(ctx.readCodesignOutput).toHaveBeenCalledTimes(1);
    expect(snapshot.manualDownloadRequired).toBe(true);
    expect(snapshot.releasePageUrl).toBe(DESKTOP_RELEASES_PAGE_URL);
    expect(autoUpdater.autoDownload).toBe(false);
    expect(send).toHaveBeenCalledWith("updater:update-available", {
      version: "9.1.0",
      releaseNotes: "notes",
      manualDownloadRequired: true,
      releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
    });
  });

  it("treats an unpackaged darwin app as a manual download without calling codesign", async () => {
    ctx.isPackaged = false;
    setupAutoUpdater(() => null);

    const snapshot = await invokeIpc<UpdaterSnapshot>("updater:get-snapshot");

    expect(ctx.readCodesignOutput).not.toHaveBeenCalled();
    expect(snapshot.manualDownloadRequired).toBe(process.platform === "darwin");
    expect(autoUpdater.autoDownload).toBe(process.platform !== "darwin");
  });
});
