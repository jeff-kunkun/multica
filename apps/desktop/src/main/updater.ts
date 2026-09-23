import { autoUpdater, type UpdateDownloadedEvent } from "electron-updater";
import { app, type BrowserWindow, ipcMain } from "electron";
import type {
  ManualUpdateCheckResult,
  ReleaseChannel,
  UpdaterPreferences,
} from "../shared/updater-types";
import {
  DEFAULT_UPDATER_PREFERENCES,
  loadUpdaterPreferences,
  saveUpdaterPreferences,
  updaterPreferencesPath,
} from "./updater-preferences";

// Silent background updates: electron-updater downloads on its own as soon
// as `update-available` fires; we only surface UI when the package is fully
// downloaded and ready to install on next quit.
autoUpdater.autoDownload = true;
autoUpdater.autoInstallOnAppQuit = true;

interface ChannelConfigurableUpdater {
  channel: string | null;
  allowDowngrade: boolean;
  allowPrerelease: boolean;
}

/**
 * `vX.Y.Z-test.N` is the test line. A distance suffix after that tag
 * (`0.5.5-test.3-2-gabcdef`) is still that line. Stable tags and the
 * describe form `0.5.4-14-gabcdef` are not.
 */
export function isTestReleaseVersion(version: string): boolean {
  const match = version
    .replace(/^v/, "")
    .match(/^\d+\.\d+\.\d+(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?/);
  if (!match?.[1]) return false;
  return match[1].split(".")[0] === "test";
}

/**
 * Feed name shared with `publishChannelForTarget` in scripts/package.mjs.
 * `null` is the untouched electron-updater default (`latest` / `latest-mac.yml`
 * / `latest-linux*.yml`). The arch-specific stable names already installed
 * clients request — `latest-x64`, `latest-arm64` — stay byte-for-byte.
 *
 * The test channel is `test` on every platform and architecture, and that is
 * not a style choice — electron-updater's GitHub provider leaves us no other
 * name (`out/providers/GitHubProvider.js`, 6.8.3):
 *
 *   1. Tag discovery. With `allowPrerelease` on, the provider walks the
 *      releases atom feed and only accepts a tag whose own prerelease segment
 *      equals `updater.channel` (or is `alpha`/`beta` when the channel itself
 *      is `alpha`/`beta`). Our tags are `vX.Y.Z-test.N`, so `test` matches and
 *      `test-arm64` matches nothing at all — the provider then throws
 *      `ERR_UPDATER_NO_PUBLISHED_VERSIONS` and the channel is simply dead.
 *   2. Manifest name. Having picked that tag, the provider *overwrites* the
 *      channel with `getCustomChannelName(semver.prerelease(tag)[0])`, i.e.
 *      `test` plus its own platform suffix, and requests `test.yml` (Windows),
 *      `test-mac.yml` (macOS), `test-linux[-arch].yml` (Linux). Whatever we
 *      put in `updater.channel` is discarded at this point.
 *
 * So on Linux electron-updater still separates the architectures for us, while
 * on Windows and macOS the test channel is necessarily one manifest per
 * platform: `test.yml` is the Windows x64 build and `test-mac.yml` the macOS
 * arm64 build — the architectures this fork actually releases. package.mjs
 * publishes the secondary architectures under `test-arm64` / `test-x64` so
 * they cannot clobber the shared manifest; nothing requests those names.
 *
 * Stable is unaffected: it runs with `allowPrerelease` off, where the provider
 * honours `updater.channel` verbatim.
 */
export function feedNameForReleaseChannel(
  releaseChannel: ReleaseChannel,
  platform: NodeJS.Platform,
  arch: string,
): string | null {
  if (releaseChannel === "test") return "test";
  if (platform === "win32" && arch === "arm64") return "latest-arm64";
  if (platform === "darwin" && arch === "x64") return "latest-x64";
  return null;
}

export function shouldAllowStableDowngrade(
  releaseChannel: ReleaseChannel,
  currentVersion: string,
): boolean {
  // `0.5.5-test.3` → `0.5.4` is a downgrade. Leaving this off traps the user
  // on the test line. It is not the AppUpdater.channel setter's side effect:
  // that flag is assigned explicitly below, after the setter runs.
  return releaseChannel === "stable" && isTestReleaseVersion(currentVersion);
}

export function applyReleaseChannel(
  updater: ChannelConfigurableUpdater,
  releaseChannel: ReleaseChannel,
  currentVersion: string,
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): void {
  const feed = feedNameForReleaseChannel(releaseChannel, platform, arch);
  // Assigning `.channel` sets allowDowngrade to true. Once it is a string,
  // a later `null` throws, so the default stable feed is spelled `latest`
  // only after some other feed was selected. `latest` still resolves to
  // `latest-mac.yml` / `latest.yml` / `latest-linux*.yml`.
  if (feed != null) {
    updater.channel = feed;
  } else if (updater.channel != null) {
    updater.channel = "latest";
  }
  updater.allowDowngrade = shouldAllowStableDowngrade(
    releaseChannel,
    currentVersion,
  );
  updater.allowPrerelease = releaseChannel === "test";
}

// Pin the architecture feed before preferences load. The saved channel is
// applied again once preferences resolve, before the first check.
applyReleaseChannel(autoUpdater, "stable", "0.0.0");

const STARTUP_CHECK_DELAY_MS = 5_000;
const PERIODIC_CHECK_INTERVAL_MS = 60 * 60 * 1000; // 1 hour

type RendererChannel =
  | "updater:update-available"
  | "updater:download-progress"
  | "updater:update-downloaded";

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

// Single-flight guard around checkForUpdates(). With autoDownload=true the
// startup, periodic, and manual triggers can all kick off downloads, and
// overlapping calls have caused duplicate download warnings in the past
// (see electronjs.org/docs/latest/api/auto-updater). Coalesce concurrent
// callers onto the same in-flight promise.
let inFlightCheck: Promise<unknown> | null = null;
function checkForUpdatesOnce(): Promise<unknown> {
  if (inFlightCheck) return inFlightCheck;
  const p = autoUpdater
    .checkForUpdates()
    .then((result) => {
      // checkForUpdates resolves as soon as metadata is fetched; the actual
      // download (when autoDownload=true) is exposed on result.downloadPromise.
      // Without a handler a download failure becomes an unhandled rejection
      // in the main process — Node may terminate it on future versions.
      void (result as { downloadPromise?: Promise<unknown> } | null)?.downloadPromise?.catch(
        (err) => {
          console.error("Failed to download update:", err);
        },
      );
      return result;
    })
    .finally(() => {
      if (inFlightCheck === p) inFlightCheck = null;
    });
  inFlightCheck = p;
  return p;
}

export function setupAutoUpdater(getMainWindow: () => BrowserWindow | null): void {
  const preferencesFilePath = updaterPreferencesPath(app.getPath("userData"));
  let automaticUpdatesEnabled =
    DEFAULT_UPDATER_PREFERENCES.automaticUpdates;
  let releaseChannel: ReleaseChannel =
    DEFAULT_UPDATER_PREFERENCES.releaseChannel;
  let startupCheckElapsed = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  const currentPreferences = (): UpdaterPreferences => ({
    automaticUpdates: automaticUpdatesEnabled,
    releaseChannel,
  });
  const preferencesReady = loadUpdaterPreferences(preferencesFilePath).then(
    (preferences) => {
      automaticUpdatesEnabled = preferences.automaticUpdates;
      releaseChannel = preferences.releaseChannel;
      applyReleaseChannel(autoUpdater, releaseChannel, app.getVersion());
      return preferences;
    },
  );

  const runAutomaticCheck = (errorMessage: string): void => {
    void preferencesReady
      .then(() => {
        if (!automaticUpdatesEnabled) return;
        return checkForUpdatesOnce();
      })
      .catch((err) => {
        console.error(errorMessage, err);
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
        runAutomaticCheck("Failed to check for updates:");
      }, STARTUP_CHECK_DELAY_MS);
    }
    if (periodicTimer === null) {
      // Background poll so long-running sessions still pick up new releases
      // without requiring the user to restart the app.
      periodicTimer = setInterval(() => {
        runAutomaticCheck("Periodic update check failed:");
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
    // Forwarded for renderer-side state tracking only; the notification UI
    // does not render an "available" affordance with autoDownload=true.
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

  autoUpdater.on("error", (err) => {
    console.error("Auto-updater error:", err);
  });

  // Retained for IPC back-compat with older renderer bundles. With
  // autoDownload=true the renderer no longer triggers this path.
  ipcMain.handle("updater:download", () => {
    return autoUpdater.downloadUpdate();
  });

  ipcMain.handle("updater:install", () => {
    autoUpdater.quitAndInstall(false, true);
  });

  ipcMain.handle(
    "updater:get-preferences",
    async (): Promise<UpdaterPreferences> => {
      await preferencesReady;
      return currentPreferences();
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
      automaticUpdatesEnabled = enabled;
      const preferences = currentPreferences();
      await saveUpdaterPreferences(preferencesFilePath, preferences);

      if (!enabled) {
        cancelBackgroundChecks();
      } else if (!wasEnabled) {
        // If the startup check has already passed while the preference was off,
        // enabling it should take effect now instead of waiting up to one hour.
        if (startupCheckElapsed) {
          runAutomaticCheck("Failed to check for updates:");
        }
        scheduleBackgroundChecks();
      }

      return preferences;
    },
  );

  ipcMain.handle(
    "updater:set-release-channel",
    async (_event, channel: unknown): Promise<UpdaterPreferences> => {
      if (channel !== "stable" && channel !== "test") {
        throw new TypeError('releaseChannel must be "stable" or "test"');
      }

      await preferencesReady;
      releaseChannel = channel;
      const preferences = currentPreferences();
      await saveUpdaterPreferences(preferencesFilePath, preferences);
      applyReleaseChannel(autoUpdater, releaseChannel, app.getVersion());
      // A check already in flight is for the previous feed. Wait it out, then
      // look up the feed just selected — don't wait for the hourly poll.
      const pending = inFlightCheck;
      const recheck = () => {
        void checkForUpdatesOnce().catch((err) => {
          console.error(
            "Failed to check for updates after channel change:",
            err,
          );
        });
      };
      if (pending) void pending.finally(recheck);
      else recheck();
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
      // staged rollouts, downgrades, and minimum-system-version gates — in
      // those cases updateInfo.version differs from app.getVersion() but no
      // `update-available` event fires, so showing "available" here would
      // promise a download prompt that never appears.
      return {
        ok: true,
        currentVersion,
        latestVersion: result?.updateInfo.version ?? currentVersion,
        available: result?.isUpdateAvailable ?? false,
      };
    } catch (err) {
      return {
        ok: false,
        error: err instanceof Error ? err.message : String(err),
      };
    }
  });

  // Initial check shortly after startup so we don't block boot, plus a
  // background poll for long-running sessions. Both are torn down when the
  // user disables automatic updates and re-armed when they turn them back on.
  scheduleBackgroundChecks();
}
