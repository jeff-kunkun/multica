// Tests for scripts/verify-release-assets.mjs (DENE-353).
//
// The point of the script is that it FAILS when an asset the publisher
// claimed to upload is not actually downloadable, so the two cases that
// matter most here are "a captured Release payload is missing a locally
// built artifact" and "an asset is stuck in the starter state". Both are
// driven end to end through the CLI with `--response-file`, since a
// verification step that only works when its pure functions are called
// directly would not have caught v0.4.56 / v0.4.57.

import { execFileSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  collectArtifacts,
  evaluateRelease,
  parseArgs,
  UsageError,
} from "./verify-release-assets.mjs";

// Resolve relative to cwd, tolerating vitest running from either the desktop
// package dir or the repo root — import.meta.url is not a file:// URL under
// the test transform, so fileURLToPath cannot be used here.
const scriptPath = [
  resolve(process.cwd(), "scripts/verify-release-assets.mjs"),
  resolve(process.cwd(), "apps/desktop/scripts/verify-release-assets.mjs"),
].find((candidate) => existsSync(candidate));

const tempDirs = [];

function makeDist(files) {
  const dir = mkdtempSync(join(tmpdir(), "multica-verify-assets-"));
  tempDirs.push(dir);
  for (const [name, size] of Object.entries(files)) {
    const path = join(dir, name);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, Buffer.alloc(size, 0x61));
  }
  return dir;
}

function writeResponse(payload) {
  const dir = mkdtempSync(join(tmpdir(), "multica-verify-response-"));
  tempDirs.push(dir);
  const path = join(dir, "release.json");
  writeFileSync(path, JSON.stringify(payload));
  return path;
}

function runCli(args) {
  try {
    const stdout = execFileSync("node", [scriptPath, ...args], {
      encoding: "utf-8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { status: 0, stdout, stderr: "" };
  } catch (error) {
    return {
      status: error.status,
      stdout: error.stdout ?? "",
      stderr: error.stderr ?? "",
    };
  }
}

afterEach(() => {
  while (tempDirs.length > 0) {
    rmSync(tempDirs.pop(), { recursive: true, force: true });
  }
});

describe("collectArtifacts", () => {
  it("collects installers, blockmaps and update feeds one level deep", () => {
    const dist = makeDist({
      "mac-arm64/multica-desktop-0.4.58-mac-arm64.dmg": 1024,
      "mac-arm64/multica-desktop-0.4.58-mac-arm64.dmg.blockmap": 16,
      "mac-arm64/multica-desktop-0.4.58-mac-arm64.zip": 900,
      "mac-arm64/multica-desktop-0.4.58-mac-arm64.zip.blockmap": 14,
      "mac-arm64/latest-mac.yml": 5,
    });
    expect(collectArtifacts(dist).map((artifact) => artifact.name)).toEqual([
      "latest-mac.yml",
      "multica-desktop-0.4.58-mac-arm64.dmg",
      "multica-desktop-0.4.58-mac-arm64.dmg.blockmap",
      "multica-desktop-0.4.58-mac-arm64.zip",
      "multica-desktop-0.4.58-mac-arm64.zip.blockmap",
    ]);
  });

  it("ignores everything the publisher never uploads", () => {
    const dist = makeDist({
      "latest-mac.yml": 5,
      "builder-debug.yml": 12,
      "builder-effective-config.yaml": 12,
      "mac-arm64/Multica.app/Contents/Info.plist": 3,
      "mac-arm64/mac/Multica.app/Contents/Info.plist": 3,
      "linux-unpacked/multica-desktop": 3,
    });
    expect(collectArtifacts(dist).map((artifact) => artifact.name)).toEqual([
      "latest-mac.yml",
    ]);
  });
});

describe("evaluateRelease", () => {
  const artifacts = [
    { name: "multica-desktop-0.4.58-mac-arm64.dmg", size: 1024 },
    { name: "latest-mac.yml", size: 5 },
  ];

  it("accepts a release holding every artifact at the local size", () => {
    const problems = evaluateRelease({
      artifacts,
      required: ["latest-mac.yml"],
      tag: "v0.4.58",
      release: {
        tag_name: "v0.4.58",
        draft: false,
        assets: [
          { name: "multica-desktop-0.4.58-mac-arm64.dmg", state: "uploaded", size: 1024 },
          { name: "latest-mac.yml", state: "uploaded", size: 5 },
          // Upstream releases also carry the GoReleaser CLI binaries.
          { name: "multica_0.4.58_darwin_arm64.tar.gz", state: "uploaded", size: 42 },
        ],
      },
    });
    expect(problems).toEqual([]);
  });

  it("names a locally built artifact that never reached the release", () => {
    const problems = evaluateRelease({
      artifacts,
      tag: "v0.4.58",
      release: {
        tag_name: "v0.4.58",
        draft: false,
        assets: [{ name: "latest-mac.yml", state: "uploaded", size: 5 }],
      },
    });
    expect(problems).toEqual([
      "multica-desktop-0.4.58-mac-arm64.dmg: missing from release v0.4.58 (built locally, 1024 bytes)",
    ]);
  });

  it("rejects an asset that is still in the starter state", () => {
    const problems = evaluateRelease({
      artifacts,
      tag: "v0.4.58",
      release: {
        tag_name: "v0.4.58",
        draft: false,
        assets: [
          { name: "multica-desktop-0.4.58-mac-arm64.dmg", state: "starter", size: 0 },
          { name: "latest-mac.yml", state: "uploaded", size: 5 },
        ],
      },
    });
    expect(problems).toEqual([
      'multica-desktop-0.4.58-mac-arm64.dmg: release state is "starter", expected "uploaded"',
    ]);
  });

  it("rejects an asset whose uploaded size is not the local size", () => {
    const problems = evaluateRelease({
      artifacts,
      tag: "v0.4.58",
      release: {
        tag_name: "v0.4.58",
        draft: false,
        assets: [
          { name: "multica-desktop-0.4.58-mac-arm64.dmg", state: "uploaded", size: 512 },
          { name: "latest-mac.yml", state: "uploaded", size: 5 },
        ],
      },
    });
    expect(problems).toEqual([
      "multica-desktop-0.4.58-mac-arm64.dmg: release size 512 bytes does not match local 1024 bytes",
    ]);
  });

  it("reports a missing release, a draft release and a required-only asset", () => {
    expect(evaluateRelease({ artifacts: [], tag: "v0.4.58", release: null })).toEqual([
      "release v0.4.58 was not found on GitHub",
    ]);
    expect(
      evaluateRelease({
        artifacts: [],
        tag: "v0.4.58",
        release: { tag_name: "v0.4.58", draft: true, assets: [] },
      }),
    ).toEqual(["release v0.4.58 is still a draft"]);
    expect(
      evaluateRelease({
        artifacts: [],
        required: ["latest-mac.yml"],
        tag: "v0.4.58",
        release: { tag_name: "v0.4.58", draft: false, assets: [] },
      }),
    ).toEqual(["latest-mac.yml: required asset missing from release v0.4.58"]);
  });
});

describe("parseArgs", () => {
  it("defaults repo, tag and token from the CI environment", () => {
    const options = parseArgs([], {
      GITHUB_REPOSITORY: "jeff-kunkun/multica",
      GITHUB_REF_NAME: "v0.4.58",
      GH_TOKEN: "secret",
    });
    expect(options).toMatchObject({
      repo: "jeff-kunkun/multica",
      tag: "v0.4.58",
      token: "secret",
      dist: "dist",
      required: [],
    });
  });

  it("collects repeated --require flags", () => {
    const options = parseArgs(
      ["--dist", "out", "--require", "a.yml", "--require", "b.dmg"],
      {},
    );
    expect(options.dist).toBe("out");
    expect(options.required).toEqual(["a.yml", "b.dmg"]);
  });

  it("rejects an unknown flag and a flag without a value", () => {
    expect(() => parseArgs(["--nope"], {})).toThrow(UsageError);
    expect(() => parseArgs(["--dist"], {})).toThrow(UsageError);
  });
});

describe("cli", () => {
  it("exits 1 and names the asset the captured release is missing", () => {
    const dist = makeDist({
      "mac-arm64/multica-desktop-0.4.57-mac-arm64.dmg": 230712949,
      "mac-arm64/multica-desktop-0.4.57-mac-arm64.zip": 220931959,
      "mac-arm64/multica-desktop-0.4.57-mac-arm64.dmg.blockmap": 241264,
      "mac-arm64/multica-desktop-0.4.57-mac-arm64.zip.blockmap": 232695,
      "mac-arm64/latest-mac.yml": 537,
    });
    // The real v0.4.57 payload: the feed and both blockmaps made it, the
    // installers never did.
    const response = writeResponse({
      tag_name: "v0.4.57",
      draft: false,
      assets: [
        { name: "latest-mac.yml", state: "uploaded", size: 537 },
        { name: "multica-desktop-0.4.57-mac-arm64.dmg.blockmap", state: "uploaded", size: 241264 },
        { name: "multica-desktop-0.4.57-mac-arm64.zip.blockmap", state: "uploaded", size: 232695 },
      ],
    });

    const result = runCli([
      "--tag",
      "v0.4.57",
      "--dist",
      dist,
      "--require",
      "latest-mac.yml",
      "--response-file",
      response,
    ]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain(
      "multica-desktop-0.4.57-mac-arm64.dmg: missing from release v0.4.57",
    );
    expect(result.stderr).toContain(
      "multica-desktop-0.4.57-mac-arm64.zip: missing from release v0.4.57",
    );
    expect(result.stderr).not.toContain("latest-mac.yml: missing");
  });

  it("exits 0 when the captured release carries every local artifact", () => {
    const dist = makeDist({
      "mac-arm64/multica-desktop-0.4.58-mac-arm64.dmg": 64,
      "mac-arm64/latest-mac.yml": 7,
    });
    const response = writeResponse({
      tag_name: "v0.4.58",
      draft: false,
      assets: [
        { name: "multica-desktop-0.4.58-mac-arm64.dmg", state: "uploaded", size: 64 },
        { name: "latest-mac.yml", state: "uploaded", size: 7 },
      ],
    });

    const result = runCli([
      "--tag",
      "v0.4.58",
      "--dist",
      dist,
      "--require",
      "latest-mac.yml",
      "--response-file",
      response,
    ]);

    expect(result.status).toBe(0);
    expect(result.stdout).toContain("verified 2 artifact(s)");
  });

  it("exits 2 on a usage error instead of a false pass", () => {
    const result = runCli(["--dist"]);
    expect(result.status).toBe(2);
    expect(result.stderr).toContain("usage:");
  });
});
