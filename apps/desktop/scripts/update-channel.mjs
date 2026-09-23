/**
 * One naming pact for the two Desktop release lines.
 *
 * Test tags (`vX.Y.Z-test.N`, cut from `kun`) publish electron-updater's
 * `beta` feed. Stable tags (`vX.Y.Z`, cut from `release`) publish `latest`.
 * `package.mjs` writes `-c.publish.channel` from `publishChannelOverride`.
 * `updater.ts` reads the same stem from `feedStem`. The yml filename is that
 * stem plus the platform suffix electron-builder and electron-updater already
 * agree on (`-mac`, `-linux`, `-linux-arm64`, empty on Windows).
 *
 * The tag says `test` and the file says `beta` on purpose. electron-updater's
 * GitHub provider only treats the prerelease ids `alpha` and `beta` as
 * channels, so a `-test.N` tag is not a feed it can walk. The app therefore
 * points the test line at one release's generic feed (see updater.ts) and
 * keeps the yml name on `beta`, which is the name both tools already share.
 */

export const UPDATE_REPOSITORY = {
  owner: "jeff-kunkun",
  repo: "multica",
};

const TEST_TAG = /^v\d+\.\d+\.\d+-test\.\d+$/;
const TEST_TRAIN = /^\d+\.\d+\.\d+-test(?:\.|$)/;

export function normalizeUpdatePlatform(platform) {
  if (platform === "mac" || platform === "darwin") return "mac";
  if (platform === "win" || platform === "win32") return "win";
  if (platform === "linux") return "linux";
  return platform;
}

export function isReleaseChannel(value) {
  return value === "stable" || value === "test";
}

/** A version cut from a `vX.Y.Z-test.N` tag, including later `git describe` dirt. */
export function isTestTrainVersion(version) {
  if (typeof version !== "string" || version.length === 0) return false;
  return TEST_TRAIN.test(version.replace(/^v/, ""));
}

export function isExactTestTag(tag) {
  return typeof tag === "string" && TEST_TAG.test(tag);
}

/**
 * Channel stem shared by electron-builder and electron-updater.
 * Arch suffixes exist only where the two tools do not add one themselves:
 * Windows arm64 (`latest-arm64.yml`) and macOS x64 (`latest-x64-mac.yml`).
 * Linux arch stays out of the stem; both tools append `-linux` / `-linux-arm64`.
 */
export function feedStem({ releaseChannel, platform, arch }) {
  const prefix = releaseChannel === "test" ? "beta" : "latest";
  const normalized = normalizeUpdatePlatform(platform);
  if (normalized === "win" && arch === "arm64") return `${prefix}-arm64`;
  if (normalized === "mac" && arch === "x64") return `${prefix}-x64`;
  return prefix;
}

/**
 * `-c.publish.channel` value, or null when electron-builder's default feed
 * name is already the stem we want. Stable mac arm64 / Windows x64 / Linux
 * stay null so existing `latest*.yml` clients keep their feed. A test train
 * must always override: the builder would otherwise name the file after the
 * prerelease id `test` (`test-mac.yml`) instead of `beta`.
 */
export function publishChannelOverride({ version, platform, arch }) {
  const releaseChannel = isTestTrainVersion(version) ? "test" : "stable";
  const stem = feedStem({ releaseChannel, platform, arch });
  if (releaseChannel === "stable" && stem === "latest") return null;
  return stem;
}

export function configureUpdateFeed(updater, options) {
  const stem = feedStem(options);
  const silent =
    normalizeUpdatePlatform(options.platform) === "mac"
      ? options.macSignedUpdates === true
      : true;
  // AppUpdater's channel setter forces allowDowngrade on. Set the real
  // policy after the assignment. Downgrade is only for leaving the test
  // line: 0.5.5-test.3 → the older stable 0.5.4 must still be offered.
  updater.channel = stem;
  updater.allowDowngrade =
    options.releaseChannel === "stable" && isTestTrainVersion(options.currentVersion);
  updater.allowPrerelease = options.releaseChannel === "test";
  updater.autoDownload = silent;
  updater.autoInstallOnAppQuit = silent;
  return {
    stem,
    installMode: silent ? "automatic" : "manual",
  };
}

export function githubReleaseFeedUrl(tag) {
  return `https://github.com/${UPDATE_REPOSITORY.owner}/${UPDATE_REPOSITORY.repo}/releases/download/${tag}`;
}

export function manualInstallerUrl({ version, platform, arch }) {
  const bare = String(version).replace(/^v/, "");
  const tag = bare.startsWith("v") ? bare : `v${bare}`;
  const normalized = normalizeUpdatePlatform(platform);
  const archName = arch === "ia32" ? "ia32" : arch === "arm64" ? "arm64" : "x64";
  let file;
  if (normalized === "mac") {
    file = `multica-desktop-${bare}-mac-${archName}.dmg`;
  } else if (normalized === "win") {
    file = `multica-desktop-${bare}-windows-${archName}.exe`;
  } else {
    file = `multica-desktop-${bare}-linux-${archName}.AppImage`;
  }
  return `https://github.com/${UPDATE_REPOSITORY.owner}/${UPDATE_REPOSITORY.repo}/releases/download/${tag}/${file}`;
}

function parseSemver(version) {
  if (typeof version !== "string") return null;
  const match = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(version);
  if (!match) return null;
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    pre: match[4] ? match[4].split(".") : null,
  };
}

function compareIdentifiers(left, right) {
  const leftNumeric = /^\d+$/.test(left);
  const rightNumeric = /^\d+$/.test(right);
  if (leftNumeric && rightNumeric) {
    const diff = Number(left) - Number(right);
    return diff < 0 ? -1 : diff > 0 ? 1 : 0;
  }
  if (leftNumeric) return -1;
  if (rightNumeric) return 1;
  if (left === right) return 0;
  return left < right ? -1 : 1;
}

function comparePrerelease(left, right) {
  if (left == null && right == null) return 0;
  if (left == null) return 1;
  if (right == null) return -1;
  const length = Math.max(left.length, right.length);
  for (let index = 0; index < length; index += 1) {
    if (index >= left.length) return -1;
    if (index >= right.length) return 1;
    const diff = compareIdentifiers(left[index], right[index]);
    if (diff !== 0) return diff;
  }
  return 0;
}

/** -1 when left < right, 0 when equal, 1 when left > right. Null if either side is not semver. */
export function compareSemver(left, right) {
  const a = parseSemver(left);
  const b = parseSemver(right);
  if (!a || !b) return null;
  if (a.major !== b.major) return a.major < b.major ? -1 : 1;
  if (a.minor !== b.minor) return a.minor < b.minor ? -1 : 1;
  if (a.patch !== b.patch) return a.patch < b.patch ? -1 : 1;
  return comparePrerelease(a.pre, b.pre);
}

/**
 * Highest published `vX.Y.Z-test.N` tag. Drafts are skipped. Anything that
 * is not an exact test tag (stable releases, other prereleases) is skipped.
 */
export function selectTestReleaseTag(releases) {
  let best = null;
  for (const release of releases ?? []) {
    if (!release || release.draft === true) continue;
    const tag = release.tag_name;
    if (!isExactTestTag(tag)) continue;
    if (best == null || compareSemver(tag, best) === 1) best = tag;
  }
  return best;
}
