// @vitest-environment node
//
// Contract test against the real electron-updater GitHub provider.
//
// The update channel is a two-sided name: `publishChannelForTarget` in
// scripts/package.mjs decides which `*.yml` a release carries, and
// `feedNameForReleaseChannel` decides which one an installed client asks for.
// Unit-testing those two against each other only proves they agree with our
// own idea of the provider — the mistake that first shipped a `beta*.yml` test
// channel while electron-updater was requesting `test*.yml`. So this file asks
// the installed electron-updater to resolve an update the way it will in
// production, against a stubbed GitHub, and asserts the files it reaches for.
import { describe, expect, it, vi } from "vitest";

// updater.ts pulls in the Electron main process at import time; only its two
// pure channel helpers are under test here. The deep provider import below is
// the real implementation either way — mocking the package entry point does not
// touch it.
vi.mock("electron", () => ({
  app: { getVersion: () => "0.0.0", getPath: () => "" },
  BrowserWindow: class BrowserWindow {},
  ipcMain: { handle: vi.fn() },
}));
vi.mock("electron-updater", () => ({
  autoUpdater: { channel: null, allowDowngrade: false, allowPrerelease: false },
}));
// electron-log/main loads the Electron binary on import, which a node-environment
// test has no business doing.
vi.mock("electron-log/main", () => ({
  default: { warn: vi.fn(), error: vi.fn(), transports: { file: { getFile: () => null } } },
}));

import { GitHubProvider } from "electron-updater/out/providers/GitHubProvider.js";
import type { AppUpdater } from "electron-updater";
import type { ProviderRuntimeOptions } from "electron-updater/out/providers/Provider.js";
import { feedNameForReleaseChannel } from "./updater";
import type { ReleaseChannel } from "../shared/updater-types";

const OWNER = "jeff-kunkun";
const REPO = "multica";

/** The atom feed GitHub serves at /<owner>/<repo>/releases.atom, newest first. */
function releasesAtom(tags: readonly string[]): string {
  const entries = tags
    .map(
      (tag) => `<entry>
    <title>${tag}</title>
    <link href="https://github.com/${OWNER}/${REPO}/releases/tag/${tag}"/>
    <content>notes</content>
  </entry>`,
    )
    .join("\n  ");
  return `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  ${entries}
</feed>`;
}

function manifestYaml(version: string): string {
  return [
    `version: ${version}`,
    "files:",
    `  - url: Multica-${version}.zip`,
    "    sha512: c2hh",
    "    size: 1",
    `path: Multica-${version}.zip`,
    "sha512: c2hh",
    "releaseDate: '2026-09-23T00:00:00.000Z'",
  ].join("\n");
}

interface ResolveArgs {
  platform: NodeJS.Platform;
  arch: string;
  releaseChannel: ReleaseChannel;
  currentVersion: string;
  /** Release tags GitHub knows about, newest first. */
  tags: readonly string[];
  /** Manifest file names each tag's release actually carries. */
  assets: Readonly<Record<string, readonly string[]>>;
  /**
   * What `GET /releases/latest` answers. GitHub omits pre-releases there, so
   * the default is the newest tag without a prerelease segment; a case that
   * wants to model an un-flagged test release names it explicitly.
   */
  latestReleaseTag?: string;
}

interface ResolveResult {
  /** Every `<tag>/<file>` the provider asked for, in order. */
  requested: string[];
  version: string | null;
  error: Error | null;
}

/**
 * Drive `GitHubProvider.getLatestVersion()` with the updater state
 * `applyReleaseChannel` installs for `releaseChannel`, and record which release
 * manifests it requests. A manifest missing from `assets` answers 404, as
 * GitHub would.
 */
async function resolveUpdate({
  platform,
  arch,
  releaseChannel,
  currentVersion,
  tags,
  assets,
  latestReleaseTag = tags.find((tag) => !tag.includes("-")),
}: ResolveArgs): Promise<ResolveResult> {
  const requested: string[] = [];
  const executor = {
    request: async (options: { path?: string }) => {
      const path = options.path ?? "";
      if (path.endsWith(".atom")) return releasesAtom(tags);
      if (path.endsWith("/releases/latest")) {
        if (latestReleaseTag == null) throw new Error("no latest release");
        return JSON.stringify({ tag_name: latestReleaseTag });
      }
      const download = /\/releases\/download\/([^/]+)\/([^?]+)/.exec(path);
      if (download == null) throw new Error(`unexpected request: ${path}`);
      const [, tag, file] = download;
      requested.push(`${tag}/${file}`);
      if (!assets[tag]?.includes(file)) throw new Error(`404 ${tag}/${file}`);
      return manifestYaml(tag.replace(/^v/, ""));
    },
  };

  // getLatestVersion() reads five fields off the updater and one executor off
  // the runtime options. Standing up a real AppUpdater would drag in the
  // Electron main process, which is the thing this test exists to stay out of.
  const provider = new GitHubProvider(
    { provider: "github", owner: OWNER, repo: REPO },
    {
      channel: feedNameForReleaseChannel(releaseChannel, platform, arch),
      allowPrerelease: releaseChannel === "test",
      allowDowngrade: false,
      currentVersion,
      fullChangelog: false,
    } as unknown as AppUpdater,
    {
      platform,
      executor,
      isUseMultipleRangeRequest: false,
    } as unknown as ProviderRuntimeOptions,
  );

  // electron-updater builds the Linux manifest suffix from the *host* arch
  // (`process.arch`), not from anything on the provider, and TEST_UPDATER_ARCH
  // is the override it ships for exactly this. Without it every Linux case
  // here would assert the arch of whatever machine runs the suite.
  const previousArch = process.env.TEST_UPDATER_ARCH;
  process.env.TEST_UPDATER_ARCH = arch;
  try {
    const info = await provider.getLatestVersion();
    return { requested, version: info.version, error: null };
  } catch (err) {
    return { requested, version: null, error: err as Error };
  } finally {
    if (previousArch == null) delete process.env.TEST_UPDATER_ARCH;
    else process.env.TEST_UPDATER_ARCH = previousArch;
  }
}

describe("GitHub update feed contract", () => {
  const STABLE_TAGS = ["v0.5.4", "v0.5.3"] as const;
  const TEST_TAGS = ["v0.5.5-test.3", "v0.5.4", "v0.5.3"] as const;

  // These names are what already-installed clients are polling right now.
  // Renaming any of them cuts every existing user off from updates on the
  // spot, so they are spelled out rather than derived.
  it.each([
    { platform: "darwin" as const, arch: "arm64", file: "latest-mac.yml" },
    { platform: "darwin" as const, arch: "x64", file: "latest-x64-mac.yml" },
    { platform: "win32" as const, arch: "x64", file: "latest.yml" },
    { platform: "win32" as const, arch: "arm64", file: "latest-arm64.yml" },
    { platform: "linux" as const, arch: "x64", file: "latest-linux.yml" },
    { platform: "linux" as const, arch: "arm64", file: "latest-linux-arm64.yml" },
  ])(
    "stable on $platform/$arch still asks for $file",
    async ({ platform, arch, file }) => {
      const { requested, version, error } = await resolveUpdate({
        platform,
        arch,
        releaseChannel: "stable",
        currentVersion: "0.5.3",
        tags: STABLE_TAGS,
        assets: { "v0.5.4": [file] },
      });

      expect(error).toBeNull();
      expect(requested).toEqual([`v0.5.4/${file}`]);
      expect(version).toBe("0.5.4");
    },
  );

  // A pre-release tag makes the provider discard `updater.channel` and rebuild
  // the name from the tag's own prerelease segment, which is why the publish
  // side has to spell the prefix `test`. Windows and macOS lose the arch
  // suffix on that path; the note on feedNameForReleaseChannel explains why
  // that is not something we get to choose.
  it.each([
    { platform: "darwin" as const, arch: "arm64", file: "test-mac.yml" },
    { platform: "darwin" as const, arch: "x64", file: "test-mac.yml" },
    { platform: "win32" as const, arch: "x64", file: "test.yml" },
    { platform: "win32" as const, arch: "arm64", file: "test.yml" },
    { platform: "linux" as const, arch: "x64", file: "test-linux.yml" },
    { platform: "linux" as const, arch: "arm64", file: "test-linux-arm64.yml" },
  ])(
    "test on $platform/$arch asks for $file",
    async ({ platform, arch, file }) => {
      const { requested, version, error } = await resolveUpdate({
        platform,
        arch,
        releaseChannel: "test",
        currentVersion: "0.5.4",
        tags: TEST_TAGS,
        assets: { "v0.5.5-test.3": [file] },
      });

      expect(error).toBeNull();
      expect(requested).toEqual([`v0.5.5-test.3/${file}`]);
      expect(version).toBe("0.5.5-test.3");
    },
  );

  it("keeps the stable line on the newest stable tag while a test tag is newer", async () => {
    const { requested, version, error } = await resolveUpdate({
      platform: "darwin",
      arch: "arm64",
      releaseChannel: "stable",
      currentVersion: "0.5.3",
      tags: TEST_TAGS,
      assets: {
        "v0.5.5-test.3": ["test-mac.yml"],
        "v0.5.4": ["latest-mac.yml"],
      },
    });

    expect(error).toBeNull();
    expect(requested).toEqual(["v0.5.4/latest-mac.yml"]);
    expect(version).toBe("0.5.4");
  });

  it("finds nothing when the test release publishes any other prefix", async () => {
    // The exact bug this file exists to catch: the release carries
    // `beta-mac.yml`, the provider asks for `test-mac.yml`, and the test
    // channel is dead. The second request is electron-updater's own fallback
    // to the default channel, which a test release does not carry either.
    const { requested, version } = await resolveUpdate({
      platform: "darwin",
      arch: "arm64",
      releaseChannel: "test",
      currentVersion: "0.5.4",
      tags: TEST_TAGS,
      assets: { "v0.5.5-test.3": ["beta-mac.yml"] },
    });

    expect(requested).toEqual([
      "v0.5.5-test.3/test-mac.yml",
      "v0.5.5-test.3/latest-mac.yml",
    ]);
    expect(version).toBeNull();
  });

  it("dies on the stable line if a test release is not flagged pre-release", async () => {
    // What the `--prerelease` flag in the release workflows buys: without it
    // GitHub's /releases/latest returns the test release, and stable clients —
    // which resolve their tag through that endpoint — ask it for a manifest it
    // does not carry.
    const { requested, version } = await resolveUpdate({
      platform: "darwin",
      arch: "arm64",
      releaseChannel: "stable",
      currentVersion: "0.5.3",
      tags: ["v0.5.5-test.3", "v0.5.4"],
      assets: {
        "v0.5.5-test.3": ["test-mac.yml"],
        "v0.5.4": ["latest-mac.yml"],
      },
      latestReleaseTag: "v0.5.5-test.3",
    });

    expect(requested).toEqual(["v0.5.5-test.3/latest-mac.yml"]);
    expect(version).toBeNull();
  });
});
