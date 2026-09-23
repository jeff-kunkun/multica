import { autoUpdater, type UpdateDownloadedEvent } from "electron-updater";
import { app, type BrowserWindow, ipcMain, shell } from "electron";
import log from "electron-log/main";
import {
  releasePageUrl,
  type ManualUpdateCheckResult,
  type UpdateCheckRecord,
  type UpdateCheckTrigger,
  type UpdaterCapabilities,
  type UpdaterPreferences,
} from "../shared/updater-types";
import {
  DEFAULT_UPDATER_PREFERENCES,
  loadUpdaterPreferences,
  saveUpdaterPreferences,
  updaterPreferencesPath,
} from "./updater-preferences";
import { detectMacSigning, type MacSigningStatus } from "./mac-signing";

// Background updates: electron-updater downloads on its own as soon as
// `update-available` fires (see resolveCapabilities for the macOS exception,
// which flips this off when the install step cannot succeed). The renderer
// mirrors every phase — checking, available, downloading, downloaded, error —
// so nothing in this chain is silent anymore.
autoUpdater.autoDownload = true;
autoUpdater.autoInstallOnAppQuit = true;

// Windows arm64 ships its own update metadata channel because
// electron-builder's `latest.yml` is not arch-suffixed on Windows — both
// arches would otherwise collide on the same file in the GitHub Release.
// See scripts/package.mjs (builderArgsForTarget) for the publish-side half
// of this pact. Pin the channel here so arm64 clients fetch
// `latest-arm64.yml` instead of the x64 metadata.
if (process.platform === "win32" && process.arch === "arm64") {
  autoUpdater.channel = "latest-arm64";
}

interface ChannelConfigurableUpdater {
  channel: string | null;
  allowDowngrade: boolean;
}

export function configureMacX64UpdateChannel(
  updater: ChannelConfigurableUpdater,
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): void {
  if (platform !== "darwin" || arch !== "x64") return;

  // AppUpdater.channel enables allowDowngrade as a side effect. This channel
  // isolates a CPU architecture, not a release train, so preserve normal
  // monotonic version behavior after selecting the architecture feed.
  updater.channel = "latest-x64";
  updater.allowDowngrade = false;
}

// electron-builder does not architecture-suffix macOS update metadata.
// package.mjs publishes macOS x64 as `latest-x64-mac.yml`; the established
// arm64 feed and runtime path remain unchanged.
configureMacX64UpdateChannel(autoUpdater);

const STARTUP_CHECK_DELAY_MS = 5_000;
const PERIODIC_CHECK_INTERVAL_MS = 60 * 60 * 1000; // 1 hour

type RendererChannel =
  | "updater:checking"
  | "updater:check-result"
  | "updater:update-available"
  | "updater:download-progress"
  | "updater:update-downloaded"
  | "updater:error";

function isDestroyedObjectError(err: unknown): boolean {
  return err instanceof Error && err.message.includes("Object has been destroyed");
}

function sendToLiveRenderer(
  win: BrowserWindow | null,
  channel: RendererChannel,
  payload: unknown,
): void {
  if (!win || win.isDestroyed()) return;

  try {
    const { webContents } = win;
    if (webContents.isDestroyed()) return;
    webContents.send(channel, payload);
  } catch (err) {
    if (isDestroyedObjectError(err)) return;
    throw err;
  }
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Decide whether this build can install updates by itself. Only macOS has a
 * hard blocker: Squirrel.Mac validates that the downloaded bundle is signed
 * by the same Developer ID as the running app, so an ad-hoc or unsigned
 * build (what CI produces without Apple credentials) fails at install time
 * every time. We ask codesign rather than guessing from the version string.
 */
export async function resolveCapabilities(
  probe: {
    platform?: NodeJS.Platform;
    isPackaged?: boolean;
    executablePath?: string;
    detectSigning?: (executablePath: string) => Promise<MacSigningStatus>;
    logPath?: string | null;
    currentVersion?: string;
  } = {},
): Promise<UpdaterCapabilities> {
  const {
    platform = process.platform,
    isPackaged = app.isPackaged,
    executablePath = app.getPath("exe"),
    detectSigning = detectMacSigning,
    logPath = null,
  } = probe;
  const base = {
    releasePageUrl: releasePageUrl(),
    logPath,
  };

  // Dev runs never auto-update (electron-updater has no app-update.yml), so
  // there is nothing to block; report "supported" and let the check no-op.
  if (platform !== "darwin" || !isPackaged) {
    return { ...base, autoUpdateSupported: true, blocker: null };
  }

  const signing = await detectSigning(executablePath);
  if (signing === "developer-id") {
    return { ...base, autoUpdateSupported: true, blocker: null };
  }
  return { ...base, autoUpdateSupported: false, blocker: "mac-unsigned" };
}

export interface SetupAutoUpdaterOptions {
  /** Test seam: override the capability probe (signing check, platform). */
  resolveCapabilities?: () => Promise<UpdaterCapabilities>;
}

function updaterLogPath(): string | null {
  try {
    return log.transports.file.getFile().path;
  } catch {
    return null;
  }
}

export function setupAutoUpdater(
  getMainWindow: () => BrowserWindow | null,
  options: SetupAutoUpdaterOptions = {},
): void {
  // Route electron-updater's own diagnostics (feed URL, cache path, download
  // failures) to the on-disk log so a packaged build leaves evidence behind:
  // ~/Library/Logs/Multica/main.log on macOS, %APPDATA%/Multica/logs on
  // Windows, ~/.config/Multica/logs on Linux.
  autoUpdater.logger = log;

  const preferencesFilePath = updaterPreferencesPath(app.getPath("userData"));
  let automaticUpdatesEnabled =
    DEFAULT_UPDATER_PREFERENCES.automaticUpdates;
  let startupCheckElapsed = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  let lastCheck: UpdateCheckRecord | null = null;
  const preferencesReady = loadUpdaterPreferences(preferencesFilePath).then(
    (preferences) => {
      automaticUpdatesEnabled = preferences.automaticUpdates;
      return preferences;
    },
  );

  const capabilitiesReady = (
    options.resolveCapabilities ??
    (() => resolveCapabilities({ logPath: updaterLogPath() }))
  )().then((capabilities) => {
    // Don't pull a package we can never install: on an ad-hoc signed macOS
    // build the user is sent to the release page instead.
    autoUpdater.autoDownload = capabilities.autoUpdateSupported;
    if (!capabilities.autoUpdateSupported) {
      log.warn(
        `[updater] automatic install unavailable (${capabilities.blocker}); manual download only`,
      );
    }
    return capabilities;
  });

  // Single-flight guard around checkForUpdates(). With autoDownload=true the
  // startup, periodic, and manual triggers can all kick off downloads, and
  // overlapping calls have caused duplicate download warnings in the past
  // (see electronjs.org/docs/latest/api/auto-updater). Coalesce concurrent
  // callers onto the same in-flight promise.
  let inFlightCheck: Promise<UpdateCheckRecord> | null = null;
  const checkForUpdatesOnce = (
    trigger: UpdateCheckTrigger,
  ): Promise<UpdateCheckRecord> => {
    if (inFlightCheck) return inFlightCheck;
    sendToLiveRenderer(getMainWindow(), "updater:checking", { trigger });
    const p = capabilitiesReady
      .then(() => autoUpdater.checkForUpdates())
      .then((result): UpdateCheckRecord => {
        // checkForUpdates resolves as soon as metadata is fetched; the actual
        // download (when autoDownload=true) is exposed on result.downloadPromise.
        // Without a handler a download failure becomes an unhandled rejection
        // in the main process — Node may terminate it on future versions. The
        // renderer hears about it through autoUpdater's own `error` event.
        void (result as { downloadPromise?: Promise<unknown> } | null)?.downloadPromise?.catch(
          (err) => {
            log.error("[updater] download failed:", err);
          },
        );
        const info = result as
          | { updateInfo: { version: string }; isUpdateAvailable?: boolean }
          | null;
        return {
          checkedAt: new Date().toISOString(),
          trigger,
          ok: true,
          // Trust electron-updater's own decision rather than re-deriving it
          // from a version-string compare. The two diverge for pre-release
          // channels, staged rollouts, downgrades, and minimum-system-version
          // gates — in those cases updateInfo.version differs from
          // app.getVersion() but no `update-available` event fires.
          available: info?.isUpdateAvailable ?? false,
          latestVersion: info?.updateInfo.version ?? app.getVersion(),
        };
      })
      .catch((err): UpdateCheckRecord => {
        log.error(`[updater] ${trigger} check failed:`, err);
        return {
          checkedAt: new Date().toISOString(),
          trigger,
          ok: false,
          error: errorMessage(err),
        };
      })
      .then((record) => {
        lastCheck = record;
        sendToLiveRenderer(getMainWindow(), "updater:check-result", record);
        return record;
      })
      .finally(() => {
        if (inFlightCheck === p) inFlightCheck = null;
      });
    inFlightCheck = p;
    return p;
  };

  const runAutomaticCheck = (trigger: "startup" | "periodic"): void => {
    void preferencesReady.then(() => {
      if (!automaticUpdatesEnabled) return;
      return checkForUpdatesOnce(trigger);
    });
  };

  // Arm the startup + periodic background checks. Idempotent: an already-armed
  // timer is left in place so re-enabling never stacks duplicate schedules.
  const scheduleBackgroundChecks = (): void => {
    if (startupTimer === null && !startupCheckElapsed) {
      // Initial check shortly after startup so we don't block boot.
      startupTimer = setTimeout(() => {
        startupTimer = null;
        startupCheckElapsed = true;
        runAutomaticCheck("startup");
      }, STARTUP_CHECK_DELAY_MS);
    }
    if (periodicTimer === null) {
      // Background poll so long-running sessions still pick up new releases
      // without requiring the user to restart the app.
      periodicTimer = setInterval(() => {
        runAutomaticCheck("periodic");
      }, PERIODIC_CHECK_INTERVAL_MS);
    }
  };

  // Tear down the scheduled checks outright when automatic updates are turned
  // off. Relying only on an in-callback preference guard leaves the timers
  // running and lets a tick that races the preference flip still fire a check;
  // clearing them makes "disabled" mean no future background work, full stop.
  const cancelBackgroundChecks = (): void => {
    if (startupTimer !== null) {
      clearTimeout(startupTimer);
      startupTimer = null;
    }
    if (periodicTimer !== null) {
      clearInterval(periodicTimer);
      periodicTimer = null;
    }
  };

  autoUpdater.on("update-available", (info) => {
    sendToLiveRenderer(getMainWindow(), "updater:update-available", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
  });

  autoUpdater.on("download-progress", (progress) => {
    sendToLiveRenderer(getMainWindow(), "updater:download-progress", {
      percent: progress.percent,
    });
  });

  autoUpdater.on("update-downloaded", (info: UpdateDownloadedEvent) => {
    sendToLiveRenderer(getMainWindow(), "updater:update-downloaded", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
  });

  // electron-updater emits `error` for both metadata and download failures.
  // Forward it so the renderer can show a failed state with a retry, and
  // keep the full object in the on-disk log for post-mortem.
  autoUpdater.on("error", (err) => {
    log.error("[updater] error:", err);
    sendToLiveRenderer(getMainWindow(), "updater:error", {
      message: errorMessage(err),
    });
  });

  // Manual download: the "Download" / "Retry" button in the renderer. Also
  // the only download path once autoDownload is off.
  ipcMain.handle("updater:download", async () => {
    try {
      await autoUpdater.downloadUpdate();
    } catch (err) {
      // electron-updater already emitted `error` for this failure; rethrow so
      // the renderer's invoke() rejects too and the button can settle.
      throw new Error(errorMessage(err));
    }
  });

  ipcMain.handle("updater:install", () => {
    autoUpdater.quitAndInstall(false, true);
  });

  ipcMain.handle(
    "updater:get-capabilities",
    (): Promise<UpdaterCapabilities> => capabilitiesReady,
  );

  ipcMain.handle(
    "updater:get-last-check",
    (): UpdateCheckRecord | null => lastCheck,
  );

  ipcMain.handle("updater:open-log", async () => {
    const path = updaterLogPath();
    if (!path) return { success: false, error: "No updater log file" };
    // shell.openPath returns "" on success, error string on failure.
    const error = await shell.openPath(path);
    return error ? { success: false, error } : { success: true };
  });

  ipcMain.handle(
    "updater:get-preferences",
    async (): Promise<UpdaterPreferences> => {
      await preferencesReady;
      return { automaticUpdates: automaticUpdatesEnabled };
    },
  );

  ipcMain.handle(
    "updater:set-automatic-updates",
    async (_event, enabled: unknown): Promise<UpdaterPreferences> => {
      if (typeof enabled !== "boolean") {
        throw new TypeError("automaticUpdates must be a boolean");
      }

      await preferencesReady;
      const wasEnabled = automaticUpdatesEnabled;
      const preferences = { automaticUpdates: enabled };
      await saveUpdaterPreferences(preferencesFilePath, preferences);
      automaticUpdatesEnabled = enabled;

      if (!enabled) {
        cancelBackgroundChecks();
      } else if (!wasEnabled) {
        // If the startup check has already passed while the preference was off,
        // enabling it should take effect now instead of waiting up to one hour.
        if (startupCheckElapsed) {
          runAutomaticCheck("startup");
        }
        scheduleBackgroundChecks();
      }

      return preferences;
    },
  );

  ipcMain.handle("updater:check", async (): Promise<ManualUpdateCheckResult> => {
    const record = await checkForUpdatesOnce("manual");
    if (!record.ok) return { ok: false, error: record.error };
    return {
      ok: true,
      currentVersion: app.getVersion(),
      latestVersion: record.latestVersion,
      available: record.available,
    };
  });

  // Initial check shortly after startup so we don't block boot, plus a
  // background poll for long-running sessions. Both are torn down when the
  // user disables automatic updates and re-armed when they turn them back on.
  scheduleBackgroundChecks();
}
