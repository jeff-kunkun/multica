import { autoUpdater, type UpdateDownloadedEvent } from "electron-updater";
import { app, type BrowserWindow, ipcMain } from "electron";
import { appendFileSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type {
  ManualUpdateCheckResult,
  ReleaseChannel,
  UpdateErrorCode,
  UpdateInstallMode,
  UpdateSnapshot,
  UpdaterPreferences,
} from "../shared/updater-types";
import {
  configureUpdateFeed,
  githubReleaseFeedUrl,
  isReleaseChannel,
  manualInstallerUrl,
  selectTestReleaseTag,
  UPDATE_REPOSITORY,
} from "../../scripts/update-channel.mjs";
import {
  DEFAULT_UPDATER_PREFERENCES,
  loadUpdaterPreferences,
  saveUpdaterPreferences,
  updaterPreferencesPath,
} from "./updater-preferences";

const STARTUP_CHECK_DELAY_MS = 5_000;
const PERIODIC_CHECK_INTERVAL_MS = 60 * 60 * 1000; // 1 hour
const LOG_TRIM_BYTES = 1_000_000;
const LOG_KEEP_BYTES = 256_000;

type RendererChannel =
  | "updater:update-available"
  | "updater:download-progress"
  | "updater:update-downloaded"
  | "updater:state";

export interface GithubReleaseListing {
  tag_name?: string;
  draft?: boolean;
}

export interface AutoUpdaterOptions {
  platform?: NodeJS.Platform;
  arch?: string;
  /** Overrides the build-time MAIN_VITE_MAC_SIGNED_UPDATES flag. */
  macSignedUpdates?: boolean;
  listReleases?: () => Promise<GithubReleaseListing[]>;
}

interface UpdaterError extends Error {
  errorCode?: UpdateErrorCode;
}

function macSignedUpdatesFromBuild(): boolean {
  const env = import.meta.env as { MAIN_VITE_MAC_SIGNED_UPDATES?: string } | undefined;
  return env?.MAIN_VITE_MAC_SIGNED_UPDATES === "true";
}

function isDestroyedObjectError(err: unknown): boolean {
  return err instanceof Error && err.message.includes("Object has been destroyed");
}

function errorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return String(err);
}

function errorCodeOf(err: unknown): UpdateErrorCode {
  if (err instanceof Error && (err as UpdaterError).errorCode === "no_test_release") {
    return "no_test_release";
  }
  return "check_failed";
}

function isIgnorableUpdaterError(message: string): boolean {
  return message.includes("not packed") || message.includes("dev update config");
}

function formatLogValue(value: unknown): string {
  if (value instanceof Error) return value.stack || value.message;
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function createUpdaterLog(logPath: string) {
  const write = (level: string, args: unknown[]) => {
    try {
      mkdirSync(dirname(logPath), { recursive: true });
      appendFileSync(
        logPath,
        `${new Date().toISOString()} ${level} ${args.map(formatLogValue).join(" ")}\n`,
        "utf8",
      );
      const size = statSync(logPath).size;
      if (size > LOG_TRIM_BYTES) {
        const text = readFileSync(logPath, "utf8");
        writeFileSync(logPath, text.slice(-LOG_KEEP_BYTES), "utf8");
      }
    } catch {
      // A logging failure must not take the update check down with it.
    }
  };
  return {
    path: logPath,
    info: (...args: unknown[]) => write("info", args),
    warn: (...args: unknown[]) => write("warn", args),
    error: (...args: unknown[]) => write("error", args),
  };
}

async function defaultListReleases(): Promise<GithubReleaseListing[]> {
  const url = `https://api.github.com/repos/${UPDATE_REPOSITORY.owner}/${UPDATE_REPOSITORY.repo}/releases?per_page=100`;
  const response = await fetch(url, {
    headers: {
      Accept: "application/vnd.github+json",
      "User-Agent": "Multica-Desktop",
      "X-GitHub-Api-Version": "2022-11-28",
    },
  });
  if (!response.ok) {
    throw new Error(`GitHub releases request failed (${response.status})`);
  }
  const body: unknown = await response.json();
  if (!Array.isArray(body)) {
    throw new Error("GitHub releases response was not a list");
  }
  return body as GithubReleaseListing[];
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

export function setupAutoUpdater(
  getMainWindow: () => BrowserWindow | null,
  options: AutoUpdaterOptions = {},
): void {
  const platform = options.platform ?? process.platform;
  const arch = options.arch ?? process.arch;
  const macSignedUpdates = options.macSignedUpdates ?? macSignedUpdatesFromBuild();
  const listReleases = options.listReleases ?? defaultListReleases;
  const preferencesFilePath = updaterPreferencesPath(app.getPath("userData"));
  const log = createUpdaterLog(join(app.getPath("userData"), "logs", "updater.log"));
  autoUpdater.logger = log;

  let automaticUpdatesEnabled = DEFAULT_UPDATER_PREFERENCES.automaticUpdates;
  let releaseChannel: ReleaseChannel = DEFAULT_UPDATER_PREFERENCES.releaseChannel;
  let installMode: UpdateInstallMode = "manual";
  let startupCheckElapsed = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  let snapshot: UpdateSnapshot = {
    phase: "idle",
    version: null,
    percent: null,
    manualDownloadUrl: null,
    installMode,
    releaseChannel,
    error: null,
    errorCode: null,
  };

  const emitState = (): void => {
    sendToLiveRenderer(getMainWindow(), "updater:state", snapshot);
  };

  const applyFeedPolicy = () => {
    const applied = configureUpdateFeed(autoUpdater, {
      releaseChannel,
      platform,
      arch,
      currentVersion: app.getVersion(),
      macSignedUpdates,
    });
    installMode = applied.installMode;
    snapshot = { ...snapshot, installMode, releaseChannel };
    return applied;
  };

  const preferencesReady = loadUpdaterPreferences(preferencesFilePath).then(
    (preferences) => {
      automaticUpdatesEnabled = preferences.automaticUpdates;
      releaseChannel = preferences.releaseChannel;
      applyFeedPolicy();
      return preferences;
    },
  );

  const reportUpdaterError = (err: unknown, context: string): void => {
    const message = errorMessage(err);
    const errorCode = errorCodeOf(err);
    log.error(`${context}: ${message}`);
    if (isIgnorableUpdaterError(message)) return;
    snapshot = {
      ...snapshot,
      phase: "error",
      // A missing test release has nothing to download. A failed download of
      // a version we already found keeps that version's installer link.
      version: errorCode === "no_test_release" ? null : snapshot.version,
      manualDownloadUrl:
        errorCode === "no_test_release" ? null : snapshot.manualDownloadUrl,
      percent: null,
      error: errorCode === "no_test_release" ? null : message,
      errorCode,
      installMode,
      releaseChannel,
    };
    emitState();
  };

  // Single-flight guard around checkForUpdates(). Startup, periodic, and
  // manual triggers can overlap, and overlapping calls have caused duplicate
  // download warnings in the past. Coalesce concurrent callers onto the same
  // in-flight promise.
  let inFlightCheck: Promise<unknown> | null = null;
  function checkForUpdatesOnce(): Promise<unknown> {
    if (inFlightCheck) return inFlightCheck;
    const p = (async () => {
      await preferencesReady;
      const applied = applyFeedPolicy();
      if (releaseChannel === "test") {
        const tag = selectTestReleaseTag(await listReleases());
        if (!tag) {
          const error = new Error("No test release published") as UpdaterError;
          error.errorCode = "no_test_release";
          throw error;
        }
        autoUpdater.setFeedURL({
          provider: "generic",
          url: githubReleaseFeedUrl(tag),
        });
        log.info(`test feed ${tag} via ${applied.stem}`);
      } else {
        autoUpdater.setFeedURL({
          provider: "github",
          owner: UPDATE_REPOSITORY.owner,
          repo: UPDATE_REPOSITORY.repo,
        });
        log.info(`stable feed via ${applied.stem}`);
      }
      const result = await autoUpdater.checkForUpdates();
      // checkForUpdates resolves as soon as metadata is fetched; the actual
      // download (when autoDownload is on) is exposed on result.downloadPromise.
      // Without a handler a download failure becomes an unhandled rejection.
      void (result as { downloadPromise?: Promise<unknown> } | null)?.downloadPromise?.catch(
        (err) => {
          reportUpdaterError(err, "Failed to download update");
        },
      );
      return result;
    })().finally(() => {
      if (inFlightCheck === p) inFlightCheck = null;
    });
    inFlightCheck = p;
    return p;
  }

  const runAutomaticCheck = (errorMessageText: string): void => {
    void preferencesReady
      .then(() => {
        if (!automaticUpdatesEnabled) return;
        return checkForUpdatesOnce();
      })
      .catch((err) => {
        reportUpdaterError(err, errorMessageText);
      });
  };

  const scheduleBackgroundChecks = (): void => {
    if (startupTimer === null && !startupCheckElapsed) {
      startupTimer = setTimeout(() => {
        startupTimer = null;
        startupCheckElapsed = true;
        runAutomaticCheck("Failed to check for updates");
      }, STARTUP_CHECK_DELAY_MS);
    }
    if (periodicTimer === null) {
      periodicTimer = setInterval(() => {
        runAutomaticCheck("Periodic update check failed");
      }, PERIODIC_CHECK_INTERVAL_MS);
    }
  };

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
    const version = info.version;
    const manualDownloadUrl = manualInstallerUrl({ version, platform, arch });
    snapshot = {
      ...snapshot,
      phase: installMode === "automatic" ? "downloading" : "available",
      version,
      percent: installMode === "automatic" ? 0 : null,
      manualDownloadUrl,
      installMode,
      releaseChannel,
      error: null,
      errorCode: null,
    };
    log.info(`update available ${version} (${installMode}) ${manualDownloadUrl}`);
    emitState();
    sendToLiveRenderer(getMainWindow(), "updater:update-available", {
      version,
      releaseNotes: info.releaseNotes,
      manualDownloadUrl,
      installMode,
    });
  });

  autoUpdater.on("download-progress", (progress) => {
    snapshot = {
      ...snapshot,
      phase: "downloading",
      percent: progress.percent,
      error: null,
      errorCode: null,
    };
    emitState();
    sendToLiveRenderer(getMainWindow(), "updater:download-progress", {
      percent: progress.percent,
    });
  });

  autoUpdater.on("update-downloaded", (info: UpdateDownloadedEvent) => {
    snapshot = {
      ...snapshot,
      phase: "ready",
      version: info.version,
      percent: 100,
      installMode,
      releaseChannel,
      error: null,
      errorCode: null,
    };
    log.info(`update downloaded ${info.version}`);
    emitState();
    sendToLiveRenderer(getMainWindow(), "updater:update-downloaded", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
  });

  autoUpdater.on("error", (err) => {
    reportUpdaterError(err, "Auto-updater error");
  });

  ipcMain.handle("updater:download", () => {
    return autoUpdater.downloadUpdate();
  });

  ipcMain.handle("updater:install", () => {
    autoUpdater.quitAndInstall(false, true);
  });

  ipcMain.handle("updater:get-state", (): UpdateSnapshot => snapshot);

  ipcMain.handle(
    "updater:get-preferences",
    async (): Promise<UpdaterPreferences> => {
      await preferencesReady;
      return { automaticUpdates: automaticUpdatesEnabled, releaseChannel };
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
      const preferences = { automaticUpdates: enabled, releaseChannel };
      await saveUpdaterPreferences(preferencesFilePath, preferences);
      automaticUpdatesEnabled = enabled;

      if (!enabled) {
        cancelBackgroundChecks();
      } else if (!wasEnabled) {
        if (startupCheckElapsed) {
          runAutomaticCheck("Failed to check for updates");
        }
        scheduleBackgroundChecks();
      }

      return preferences;
    },
  );

  ipcMain.handle(
    "updater:set-release-channel",
    async (_event, value: unknown): Promise<UpdaterPreferences> => {
      if (!isReleaseChannel(value)) {
        throw new TypeError("releaseChannel must be stable or test");
      }

      await preferencesReady;
      const changed = value !== releaseChannel;
      releaseChannel = value;
      const preferences = {
        automaticUpdates: automaticUpdatesEnabled,
        releaseChannel,
      };
      await saveUpdaterPreferences(preferencesFilePath, preferences);
      applyFeedPolicy();
      emitState();
      if (changed) {
        log.info(`release channel -> ${value}`);
        // The in-flight check, if any, already chose a feed. Let it finish,
        // then look up the line the user just picked.
        if (inFlightCheck) await inFlightCheck.catch(() => undefined);
        void checkForUpdatesOnce().catch((err) => {
          reportUpdaterError(err, "Failed to check for updates after channel change");
        });
      }
      return preferences;
    },
  );

  ipcMain.handle("updater:check", async (): Promise<ManualUpdateCheckResult> => {
    try {
      const result = (await checkForUpdatesOnce()) as
        | { updateInfo: { version: string }; isUpdateAvailable?: boolean }
        | null;
      const currentVersion = app.getVersion();
      // Trust electron-updater's own decision rather than re-deriving it from
      // a version-string compare. The two diverge for pre-release channels,
      // staged rollouts, downgrades, and minimum-system-version gates.
      return {
        ok: true,
        currentVersion,
        latestVersion: result?.updateInfo.version ?? currentVersion,
        available: result?.isUpdateAvailable ?? false,
        installMode,
      };
    } catch (err) {
      reportUpdaterError(err, "Manual update check failed");
      return {
        ok: false,
        error: errorMessage(err),
        errorCode: errorCodeOf(err),
      };
    }
  });

  scheduleBackgroundChecks();
}
