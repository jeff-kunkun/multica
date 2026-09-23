export type ReleaseChannel = "stable" | "test";

export interface UpdaterPreferences {
  automaticUpdates: boolean;
  releaseChannel: ReleaseChannel;
}

export type ManualUpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
    }
  | { ok: false; error: string };

/** What kicked off an update check. Surfaced in Settings → Updates. */
export type UpdateCheckTrigger = "startup" | "periodic" | "manual";

/**
 * Outcome of the most recent update check, whichever trigger ran it. The
 * main process keeps the latest record so a Settings page mounted after the
 * check can still show it, and pushes each new one over `updater:check-result`.
 */
export type UpdateCheckRecord =
  | {
      checkedAt: string;
      trigger: UpdateCheckTrigger;
      ok: true;
      available: boolean;
      latestVersion: string;
    }
  | {
      checkedAt: string;
      trigger: UpdateCheckTrigger;
      ok: false;
      error: string;
    };

/** Why this build cannot install updates on its own. */
export type AutoUpdateBlocker = "mac-unsigned";

/**
 * Whether the running build can complete the download → install path by
 * itself. On macOS, Squirrel.Mac refuses to install a package whose signature
 * does not match the running app's Developer ID, so an ad-hoc signed build
 * (what CI ships) can only ever tell the user to download a release manually.
 */
export interface UpdaterCapabilities {
  autoUpdateSupported: boolean;
  blocker: AutoUpdateBlocker | null;
  /** GitHub Releases page for the fork; the manual fallback opens this. */
  releasePageUrl: string;
  /** On-disk updater log (electron-log), or null when the logger has no file. */
  logPath: string | null;
}

export interface UpdateAvailablePayload {
  version: string;
  releaseNotes?: unknown;
}

export interface UpdateDownloadProgressPayload {
  percent: number;
}

export interface UpdaterErrorPayload {
  message: string;
}

export interface UpdaterCheckingPayload {
  trigger: UpdateCheckTrigger;
}

/**
 * Source of the release page URL. Mirrors `publish.owner` / `publish.repo` in
 * `apps/desktop/electron-builder.yml`; electron-updater reads the same pair
 * from the packaged `app-update.yml` at runtime.
 */
export const RELEASE_REPO = "jeff-kunkun/multica";

export function releasePageUrl(version?: string | null): string {
  const base = `https://github.com/${RELEASE_REPO}/releases`;
  return version ? `${base}/tag/v${version.replace(/^v/, "")}` : `${base}/latest`;
}
