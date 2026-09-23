export interface UpdaterPreferences {
  automaticUpdates: boolean;
}

export type ManualUpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
    }
  | { ok: false; error: string };

/** Where an update check was triggered from. Shown with the last result. */
export type UpdateCheckSource = "startup" | "periodic" | "manual" | "reenable";

export interface UpdateCheckRecord {
  checkedAt: string;
  source: UpdateCheckSource;
  ok: boolean;
  currentVersion?: string;
  latestVersion?: string;
  available?: boolean;
  error?: string;
}

export type UpdaterPhase =
  | "idle"
  | "checking"
  | "available"
  | "downloading"
  | "downloaded"
  | "error";

export interface UpdaterErrorInfo {
  message: string;
}

export interface UpdateAvailableInfo {
  version: string;
  releaseNotes?: string;
  manualDownloadRequired: boolean;
  releasePageUrl: string;
}

export interface UpdaterSnapshot {
  phase: UpdaterPhase;
  version: string | null;
  percent: number | null;
  error: string | null;
  /**
   * True when this darwin build cannot finish a Squirrel.Mac install
   * (no Developer ID Application authority on the running bundle).
   */
  manualDownloadRequired: boolean;
  releasePageUrl: string;
  lastCheck: UpdateCheckRecord | null;
}

/** GitHub Releases page that hosts this fork's desktop packages. */
export const DESKTOP_RELEASES_PAGE_URL =
  "https://github.com/jeff-kunkun/multica/releases";

export function clampUpdatePercent(value: unknown): number {
  const percent = typeof value === "number" ? value : Number.NaN;
  if (!Number.isFinite(percent)) return 0;
  return Math.min(100, Math.max(0, percent));
}
