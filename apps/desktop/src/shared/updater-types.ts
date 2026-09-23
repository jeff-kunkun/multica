export type ReleaseChannel = "stable" | "test";

export type UpdateInstallMode = "automatic" | "manual";

export type UpdatePhase =
  | "idle"
  | "available"
  | "downloading"
  | "ready"
  | "error";

export type UpdateErrorCode = "no_test_release" | "check_failed";

export interface UpdaterPreferences {
  automaticUpdates: boolean;
  releaseChannel: ReleaseChannel;
}

/** What the main process last observed about an update. Renderers hydrate from this. */
export interface UpdateSnapshot {
  phase: UpdatePhase;
  version: string | null;
  percent: number | null;
  manualDownloadUrl: string | null;
  installMode: UpdateInstallMode;
  releaseChannel: ReleaseChannel;
  error: string | null;
  errorCode: UpdateErrorCode | null;
}

export type ManualUpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
      installMode: UpdateInstallMode;
    }
  | { ok: false; error: string; errorCode: UpdateErrorCode };
