import { autoUpdater, type UpdateDownloadedEvent } from "electron-updater";
import { app, type BrowserWindow, ipcMain } from "electron";
import {
  DESKTOP_RELEASES_PAGE_URL,
  clampUpdatePercent,
  type ManualUpdateCheckResult,
  type UpdateCheckRecord,
  type UpdateCheckSource,
  type UpdaterPreferences,
  type UpdaterSnapshot,
} from "../shared/updater-types";
import {
  DEFAULT_UPDATER_PREFERENCES,
  loadUpdaterPreferences,
  saveUpdaterPreferences,
  updaterPreferencesPath,
} from "./updater-preferences";
import { createUpdaterLogger, type UpdaterLogger } from "./updater-logger";
import {
  macAppBundlePath,
  readCodesignOutput,
  shouldRequireManualDownload,
} from "./updater-signature";

// Background download stays on for builds that can actually install the
// package. Unsigned darwin builds flip autoDownload off once the signature
// check resolves — Squirrel.Mac rejects those installs, so the renderer
// offers the release page instead of a progress bar that will fail.
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
  | "updater:update-available"
  | "updater:download-progress"
  | "updater:update-downloaded"
  | "updater:error"
  | "updater:check-result";

type CheckResult = {
  updateInfo?: { version?: string };
  isUpdateAvailable?: boolean;
  downloadPromise?: Promise<unknown>;
} | null;

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

function describeUpdaterError(err: unknown, message?: unknown): string {
  if (typeof message === "string" && message.trim()) return message.trim();
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  if (typeof err === "string" && err.trim()) return err.trim();
  return "Update failed";
}

function emptySnapshot(): UpdaterSnapshot {
  return {
    phase: "idle",
    version: null,
    percent: null,
    error: null,
    manualDownloadRequired: false,
    releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
    lastCheck: null,
  };
}

// Single-flight guard around checkForUpdates(). With autoDownload=true the
// startup, periodic, and manual triggers can all kick off downloads, and
// overlapping calls have caused duplicate download warnings in the past
// (see electronjs.org/docs/latest/api/auto-updater). Coalesce concurrent
// callers onto the same in-flight promise.
let inFlightCheck: Promise<unknown> | null = null;
let activeLogger: UpdaterLogger | null = null;
let environmentGeneration = 0;
let reportUpdaterError: (err: unknown, message?: unknown) => void = (err) => {
  console.error("Auto-updater error:", err);
};

function checkForUpdatesOnce(): Promise<unknown> {
  if (inFlightCheck) return inFlightCheck;
  const p = autoUpdater
    .checkForUpdates()
    .then((result) => {
      // checkForUpdates resolves as soon as metadata is fetched; the actual
      // download (when autoDownload=true) is exposed on result.downloadPromise.
      // Without a handler a download failure becomes an unhandled rejection
      // in the main process — Node may terminate it on future versions.
      void (result as CheckResult)?.downloadPromise?.catch((err) => {
        reportUpdaterError(err);
      });
      return result;
    })
    .finally(() => {
      if (inFlightCheck === p) inFlightCheck = null;
    });
  inFlightCheck = p;
  return p;
}

async function detectManualDownloadRequired(): Promise<boolean> {
  let signatureText: string | null = null;
  if (process.platform === "darwin" && app.isPackaged) {
    try {
      signatureText = await readCodesignOutput(macAppBundlePath(app.getPath("exe")));
    } catch (err) {
      activeLogger?.warn("Could not read the macOS code signature.", err);
      signatureText = null;
    }
  }
  return shouldRequireManualDownload({
    platform: process.platform,
    packaged: app.isPackaged,
    signatureText,
  });
}

export function setupAutoUpdater(getMainWindow: () => BrowserWindow | null): void {
  const preferencesFilePath = updaterPreferencesPath(app.getPath("userData"));
  let automaticUpdatesEnabled = DEFAULT_UPDATER_PREFERENCES.automaticUpdates;
  let startupCheckElapsed = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  const snapshot = emptySnapshot();
  const preferencesReady = loadUpdaterPreferences(preferencesFilePath).then(
    (preferences) => {
      automaticUpdatesEnabled = preferences.automaticUpdates;
      return preferences;
    },
  );

  activeLogger = createUpdaterLogger(app.getPath("logs"));
  autoUpdater.logger = activeLogger;
  autoUpdater.autoDownload = true;
  activeLogger.info("Updater logger attached.");

  const send = (channel: RendererChannel, payload: unknown): void => {
    sendToLiveRenderer(getMainWindow(), channel, payload);
  };

  reportUpdaterError = (err: unknown, message?: unknown): void => {
    const text = describeUpdaterError(err, message);
    activeLogger?.error("Auto-updater error:", text);
    snapshot.phase = "error";
    snapshot.error = text;
    send("updater:error", { message: text });
  };

  const generation = ++environmentGeneration;
  const environmentReady = detectManualDownloadRequired().then((required) => {
    // A newer setupAutoUpdater call owns the updater. Ignore this decision so
    // a late signature read cannot flip autoDownload back.
    if (generation !== environmentGeneration) return required;
    snapshot.manualDownloadRequired = required;
    autoUpdater.autoDownload = !required;
    return required;
  });

  const publishCheck = (record: UpdateCheckRecord): void => {
    snapshot.lastCheck = record;
    if (!record.ok) {
      if (snapshot.phase !== "downloading" && snapshot.phase !== "downloaded") {
        snapshot.phase = "error";
        snapshot.error = record.error ?? "Update check failed";
      }
    } else if (record.available) {
      if (record.latestVersion && !snapshot.version) {
        snapshot.version = record.latestVersion;
      }
      if (snapshot.phase === "idle" || snapshot.phase === "checking") {
        snapshot.phase = "available";
        snapshot.version = record.latestVersion ?? snapshot.version;
        snapshot.error = null;
      }
    } else if (snapshot.phase !== "downloading" && snapshot.phase !== "downloaded") {
      snapshot.phase = "idle";
      snapshot.version = null;
      snapshot.percent = null;
      snapshot.error = null;
    }
    send("updater:check-result", record);
  };

  const runAutomaticCheck = (source: UpdateCheckSource): void => {
    void preferencesReady
      .then(async () => {
        await environmentReady;
        if (!automaticUpdatesEnabled) return;
        try {
          const result = (await checkForUpdatesOnce()) as CheckResult;
          publishCheck(checkRecord(source, result));
        } catch (err) {
          const error = describeUpdaterError(err);
          activeLogger?.error(`Update check failed (${source}):`, error);
          publishCheck({
            checkedAt: new Date().toISOString(),
            source,
            ok: false,
            currentVersion: app.getVersion(),
            error,
          });
        }
      })
      .catch((err) => {
        const error = describeUpdaterError(err);
        activeLogger?.error(`Update check failed (${source}):`, error);
        publishCheck({
          checkedAt: new Date().toISOString(),
          source,
          ok: false,
          error,
        });
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
    if (snapshot.phase !== "downloading" && snapshot.phase !== "downloaded") {
      snapshot.phase = "available";
      snapshot.percent = null;
      snapshot.error = null;
    }
    snapshot.version = info.version;
    send("updater:update-available", {
      version: info.version,
      releaseNotes: typeof info.releaseNotes === "string" ? info.releaseNotes : undefined,
      manualDownloadRequired: snapshot.manualDownloadRequired,
      releasePageUrl: snapshot.releasePageUrl,
    });
  });

  autoUpdater.on("download-progress", (progress) => {
    if (snapshot.manualDownloadRequired) return;
    const percent = clampUpdatePercent(progress.percent);
    snapshot.phase = "downloading";
    snapshot.percent = percent;
    snapshot.error = null;
    send("updater:download-progress", { percent });
  });

  autoUpdater.on("update-downloaded", (info: UpdateDownloadedEvent) => {
    snapshot.phase = "downloaded";
    snapshot.version = info.version;
    snapshot.percent = 100;
    snapshot.error = null;
    send("updater:update-downloaded", {
      version: info.version,
      releaseNotes: typeof info.releaseNotes === "string" ? info.releaseNotes : undefined,
    });
  });

  autoUpdater.on("error", (err, message) => {
    reportUpdaterError(err, message);
  });

  ipcMain.handle("updater:download", () => {
    return autoUpdater.downloadUpdate();
  });

  ipcMain.handle("updater:install", () => {
    autoUpdater.quitAndInstall(false, true);
  });

  ipcMain.handle("updater:get-preferences", async (): Promise<UpdaterPreferences> => {
    await preferencesReady;
    return { automaticUpdates: automaticUpdatesEnabled };
  });

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
          runAutomaticCheck("reenable");
        }
        scheduleBackgroundChecks();
      }

      return preferences;
    },
  );

  ipcMain.handle("updater:get-snapshot", async (): Promise<UpdaterSnapshot> => {
    await environmentReady;
    return {
      ...snapshot,
      lastCheck: snapshot.lastCheck ? { ...snapshot.lastCheck } : null,
    };
  });

  ipcMain.handle("updater:check", async (): Promise<ManualUpdateCheckResult> => {
    try {
      await environmentReady;
      const result = (await checkForUpdatesOnce()) as CheckResult;
      const record = checkRecord("manual", result);
      publishCheck(record);
      return {
        ok: true,
        currentVersion: record.currentVersion ?? app.getVersion(),
        latestVersion: record.latestVersion ?? app.getVersion(),
        available: record.available ?? false,
      };
    } catch (err) {
      const error = describeUpdaterError(err);
      activeLogger?.error("Update check failed (manual):", error);
      publishCheck({
        checkedAt: new Date().toISOString(),
        source: "manual",
        ok: false,
        error,
      });
      return { ok: false, error };
    }
  });

  // Initial check shortly after startup so we don't block boot, plus a
  // background poll for long-running sessions. Both are torn down when the
  // user disables automatic updates and re-armed when they turn them back on.
  // The check itself waits until the signature decision is known, so an
  // unsigned Mac never starts a download that cannot install.
  scheduleBackgroundChecks();
}

function checkRecord(source: UpdateCheckSource, result: CheckResult): UpdateCheckRecord {
  const currentVersion = app.getVersion();
  return {
    checkedAt: new Date().toISOString(),
    source,
    ok: true,
    currentVersion,
    latestVersion: result?.updateInfo?.version ?? currentVersion,
    available: result?.isUpdateAvailable ?? false,
  };
}
