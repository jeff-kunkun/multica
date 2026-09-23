export const UPDATE_REPOSITORY: { owner: string; repo: string };

export function normalizeUpdatePlatform(
  platform: string,
): "mac" | "win" | "linux" | string;

export function isReleaseChannel(value: unknown): value is "stable" | "test";

export function isTestTrainVersion(version: string | null | undefined): boolean;

export function isExactTestTag(tag: unknown): tag is string;

export function feedStem(options: {
  releaseChannel: "stable" | "test";
  platform: string;
  arch: string;
}): string;

export function publishChannelOverride(options: {
  version: string | null | undefined;
  platform: string;
  arch: string;
}): string | null;

export function configureUpdateFeed(
  updater: {
    channel: string | null;
    allowDowngrade: boolean;
    allowPrerelease: boolean;
    autoDownload: boolean;
    autoInstallOnAppQuit: boolean;
  },
  options: {
    releaseChannel: "stable" | "test";
    platform: string;
    arch: string;
    currentVersion: string;
    macSignedUpdates: boolean;
  },
): { stem: string; installMode: "automatic" | "manual" };

export function githubReleaseFeedUrl(tag: string): string;

export function manualInstallerUrl(options: {
  version: string;
  platform: string;
  arch: string;
}): string;

export function compareSemver(left: string, right: string): -1 | 0 | 1 | null;

export function selectTestReleaseTag(
  releases: Array<{ tag_name?: string; draft?: boolean } | null> | null | undefined,
): string | null;
