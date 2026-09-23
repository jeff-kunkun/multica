import { describe, expect, it } from "vitest";
import {
  compareSemver,
  configureUpdateFeed,
  feedStem,
  githubReleaseFeedUrl,
  isTestTrainVersion,
  manualInstallerUrl,
  publishChannelOverride,
  selectTestReleaseTag,
} from "./update-channel.mjs";

describe("publish and runtime feed stems", () => {
  const cases = [
    ["0.5.5", "mac", "arm64", null, "latest"],
    ["0.5.5", "darwin", "arm64", null, "latest"],
    ["0.5.5", "mac", "x64", "latest-x64", "latest-x64"],
    ["0.5.5", "win", "x64", null, "latest"],
    ["0.5.5", "win32", "x64", null, "latest"],
    ["0.5.5", "win", "arm64", "latest-arm64", "latest-arm64"],
    ["0.5.5", "linux", "x64", null, "latest"],
    ["0.5.5", "linux", "arm64", null, "latest"],
    ["0.5.5-test.1", "mac", "arm64", "beta", "beta"],
    ["0.5.5-test.2", "mac", "x64", "beta-x64", "beta-x64"],
    ["0.5.5-test.1", "win", "x64", "beta", "beta"],
    ["0.5.5-test.1", "win", "arm64", "beta-arm64", "beta-arm64"],
    ["0.5.5-test.1", "linux", "x64", "beta", "beta"],
    ["0.5.5-test.1", "linux", "arm64", "beta", "beta"],
    ["v0.5.5-test.4", "darwin", "arm64", "beta", "beta"],
  ];

  it.each(cases)(
    "version %s on %s/%s publishes %s and runs on %s",
    (version, platform, arch, publishChannel, stem) => {
      expect(publishChannelOverride({ version, platform, arch })).toBe(publishChannel);
      expect(
        feedStem({
          releaseChannel: isTestTrainVersion(version) ? "test" : "stable",
          platform,
          arch,
        }),
      ).toBe(stem);
    },
  );

  it("does not treat a between-tags describe version as the test line", () => {
    expect(isTestTrainVersion("0.5.5-14-gf1415e96")).toBe(false);
    expect(
      publishChannelOverride({
        version: "0.5.5-14-gf1415e96",
        platform: "mac",
        arch: "arm64",
      }),
    ).toBe(null);
  });
});

describe("configureUpdateFeed", () => {
  it("keeps downgrades off for an architecture feed on the stable line", () => {
    const updater = freshUpdater();
    const result = configureUpdateFeed(updater, {
      releaseChannel: "stable",
      platform: "darwin",
      arch: "x64",
      currentVersion: "0.5.4",
      macSignedUpdates: false,
    });

    expect(result).toEqual({ stem: "latest-x64", installMode: "manual" });
    expect(updater.allowDowngrade).toBe(false);
    expect(updater.allowPrerelease).toBe(false);
    expect(updater.autoDownload).toBe(false);
  });

  it("allows the downgrade from a test build back to stable", () => {
    const updater = freshUpdater();
    configureUpdateFeed(updater, {
      releaseChannel: "stable",
      platform: "win32",
      arch: "x64",
      currentVersion: "0.5.5-test.3",
      macSignedUpdates: false,
    });

    expect(updater.channel).toBe("latest");
    expect(updater.allowDowngrade).toBe(true);
    expect(updater.allowPrerelease).toBe(false);
    expect(updater.autoDownload).toBe(true);
    expect(updater.autoInstallOnAppQuit).toBe(true);
  });

  it("follows prereleases on the test line and installs silently off macOS", () => {
    const updater = freshUpdater();
    const result = configureUpdateFeed(updater, {
      releaseChannel: "test",
      platform: "linux",
      arch: "arm64",
      currentVersion: "0.5.4",
      macSignedUpdates: false,
    });

    expect(result).toEqual({ stem: "beta", installMode: "automatic" });
    expect(updater.allowPrerelease).toBe(true);
    expect(updater.allowDowngrade).toBe(false);
  });

  it("installs macOS updates in place only when the build was signed", () => {
    const updater = freshUpdater();
    const result = configureUpdateFeed(updater, {
      releaseChannel: "stable",
      platform: "darwin",
      arch: "arm64",
      currentVersion: "0.5.4",
      macSignedUpdates: true,
    });

    expect(result.installMode).toBe("automatic");
    expect(updater.autoDownload).toBe(true);
  });
});

describe("test release selection", () => {
  it("picks the highest exact test tag and skips drafts and other lines", () => {
    expect(
      selectTestReleaseTag([
        { tag_name: "v0.5.5" },
        { tag_name: "v0.5.6-test.1" },
        { tag_name: "v0.5.6-test.2", draft: true },
        { tag_name: "v0.5.5-test.9" },
        { tag_name: "v0.5.6-beta.1" },
        { tag_name: "v0.5.4-test.1" },
      ]),
    ).toBe("v0.5.6-test.1");
  });

  it("returns null when the test line has not published anything", () => {
    expect(selectTestReleaseTag([{ tag_name: "v0.5.5" }, { draft: true }])).toBe(null);
    expect(selectTestReleaseTag(null)).toBe(null);
  });
});

describe("semver ordering used to decide an update", () => {
  it("orders the test line under the stable version of the same numbers", () => {
    expect(compareSemver("0.5.5-test.3", "0.5.5")).toBe(-1);
    expect(compareSemver("v0.5.5", "0.5.5-test.3")).toBe(1);
    expect(compareSemver("0.5.5-test.2", "0.5.5-test.10")).toBe(-1);
    expect(compareSemver("0.5.5-test.1", "0.5.4")).toBe(1);
    expect(compareSemver("0.5.10", "0.5.9")).toBe(1);
    expect(compareSemver("0.5.5", "0.5.5")).toBe(0);
  });
});

describe("manual installer urls", () => {
  it("points at the dmg, exe, or AppImage on the release that built that version", () => {
    expect(
      manualInstallerUrl({
        version: "0.5.5-test.1",
        platform: "darwin",
        arch: "arm64",
      }),
    ).toBe(
      "https://github.com/jeff-kunkun/multica/releases/download/v0.5.5-test.1/multica-desktop-0.5.5-test.1-mac-arm64.dmg",
    );
    expect(
      manualInstallerUrl({ version: "0.5.5", platform: "win32", arch: "x64" }),
    ).toBe(
      "https://github.com/jeff-kunkun/multica/releases/download/v0.5.5/multica-desktop-0.5.5-windows-x64.exe",
    );
    expect(githubReleaseFeedUrl("v0.5.5-test.1")).toBe(
      "https://github.com/jeff-kunkun/multica/releases/download/v0.5.5-test.1",
    );
  });
});

function freshUpdater() {
  return {
    channel: null,
    allowDowngrade: true,
    allowPrerelease: false,
    autoDownload: false,
    autoInstallOnAppQuit: false,
  };
}
