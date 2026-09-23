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
