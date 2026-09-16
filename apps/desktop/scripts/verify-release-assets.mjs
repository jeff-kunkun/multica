#!/usr/bin/env node
// Post-release asset verification for Desktop installers (DENE-353).
//
// "The publisher exited 0" is not evidence that the assets are downloadable.
// A dropped upload connection leaves the asset in the `starter` state, or the
// upload never lands at all, while `gh release upload` / electron-builder's
// GitHub publisher still return success. That is exactly how v0.4.56 and
// v0.4.57 shipped a `latest-mac.yml` pointing at a DMG and ZIP that were
// never uploaded: the auto-update feed advertised a release nobody could
// download.
//
// So after the upload, read the GitHub Release back through the API and
// assert, for every artifact this run produced, that it exists on the
// Release, is in the `uploaded` state, and has the exact local byte size.
// Any mismatch fails the step and names what is missing.
//
// Usage (from apps/desktop, after `node scripts/package.mjs ... --publish always`):
//
//   node scripts/verify-release-assets.mjs \
//     --repo jeff-kunkun/multica --tag v0.4.58 --dist dist \
//     --require latest-mac.yml
//
// `--response-file <path>` reads an already-captured Release API payload
// instead of calling GitHub, which is what makes this verifiable offline (and
// is how the test suite feeds it a release with a missing asset).
//
// Exit codes: 0 verified, 1 at least one problem, 2 usage / IO / API error.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Everything electron-builder uploads for a Desktop target: the installers
// themselves, their differential-update blockmaps, and the update feed
// (`latest.yml` / `latest-mac.yml` / `latest-linux.yml`, arch-suffixed on
// Windows and Linux as needed). Unpacked app directories, builder debug
// output and resources are not uploaded, so they never match.
const ARTIFACT_NAME_RE =
  /^(multica-desktop-.*\.(dmg|zip|exe|AppImage|deb|rpm|blockmap)|latest[^/]*\.yml)$/;

const API_VERSION = "2022-11-28";
// electron-builder nests artifacts one directory deep (`dist/mac-arm64/…`)
// when it is given a scoped output dir, and writes straight into `dist/`
// otherwise. Deeper nesting is the .app / unpacked payload, which is never
// uploaded, so the walk stops before descending into it.
const MAX_ARTIFACT_DEPTH = 2;

export class UsageError extends Error {}

/**
 * Collect the publishable artifacts under an electron-builder output dir,
 * as `{ name, size, path }`, sorted by name.
 */
export function collectArtifacts(distDir, depth = 0, found = new Map()) {
  let entries;
  try {
    entries = readdirSync(distDir, { withFileTypes: true });
  } catch (error) {
    throw new UsageError(
      `cannot read --dist directory ${distDir}: ${error.message}`,
    );
  }

  for (const entry of entries) {
    const full = join(distDir, entry.name);
    if (entry.isDirectory()) {
      if (
        depth + 1 >= MAX_ARTIFACT_DEPTH ||
        entry.name.endsWith(".app") ||
        entry.name.endsWith("-unpacked")
      ) {
        continue;
      }
      collectArtifacts(full, depth + 1, found);
      continue;
    }
    if (!ARTIFACT_NAME_RE.test(entry.name)) continue;
    // Two arch builds cannot share an artifact name; if they ever do, the
    // bytes would differ and the size assertion should see the real file.
    found.set(entry.name, {
      name: entry.name,
      size: statSync(full).size,
      path: full,
    });
  }

  return [...found.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Compare the local artifacts (and explicitly required names) against a
 * Release API payload. Returns a list of problems; empty means verified.
 *
 * Assets on the Release that this run did not build are ignored on purpose:
 * upstream a Release also carries the GoReleaser CLI binaries and the Helm
 * chart, and this check is about what *this* job uploaded.
 */
export function evaluateRelease({ artifacts, release, required = [], tag }) {
  const problems = [];

  if (release == null) {
    return [`release ${tag} was not found on GitHub`];
  }
  if (release.draft === true) {
    // A draft is invisible to installed clients, and electron-builder's
    // publisher skips uploads into a draft while `releaseType: release`
    // expects a published one.
    problems.push(`release ${tag} is still a draft`);
  }

  const assets = Array.isArray(release.assets) ? release.assets : null;
  if (assets === null) {
    problems.push(
      `release ${tag} payload has no assets array; cannot verify uploads`,
    );
    return problems;
  }

  const byName = new Map(assets.map((asset) => [asset.name, asset]));

  for (const artifact of artifacts) {
    const asset = byName.get(artifact.name);
    if (asset === undefined) {
      problems.push(
        `${artifact.name}: missing from release ${tag} (built locally, ${artifact.size} bytes)`,
      );
      continue;
    }
    if (asset.state !== "uploaded") {
      problems.push(
        `${artifact.name}: release state is ${JSON.stringify(asset.state)}, expected "uploaded"`,
      );
      continue;
    }
    if (asset.size !== artifact.size) {
      problems.push(
        `${artifact.name}: release size ${asset.size} bytes does not match local ${artifact.size} bytes`,
      );
    }
  }

  const checked = new Set(artifacts.map((artifact) => artifact.name));
  for (const name of required) {
    if (checked.has(name)) continue;
    if (!byName.has(name)) {
      problems.push(`${name}: required asset missing from release ${tag}`);
    }
  }

  return problems;
}

async function fetchRelease({ repo, tag, apiBase, token }) {
  if (!repo) {
    throw new UsageError("--repo is required (or set GITHUB_REPOSITORY)");
  }
  if (!tag) {
    throw new UsageError("--tag is required (or set GITHUB_REF_NAME)");
  }
  if (!token) {
    throw new UsageError("set GH_TOKEN or GITHUB_TOKEN to read the Release");
  }

  const url = `${apiBase}/repos/${repo}/releases/tags/${encodeURIComponent(tag)}`;
  const response = await fetch(url, {
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "user-agent": "multica-verify-release-assets",
      "x-github-api-version": API_VERSION,
    },
  });
  if (response.status === 404) return null;
  if (!response.ok) {
    throw new Error(
      `GitHub API ${response.status} ${response.statusText} for ${url}: ${(
        await response.text()
      ).trim()}`,
    );
  }
  return response.json();
}

export function parseArgs(argv, env = process.env) {
  const options = {
    repo: env.GITHUB_REPOSITORY ?? "",
    tag: env.GITHUB_REF_NAME ?? "",
    dist: "dist",
    apiBase: "https://api.github.com",
    token: env.GH_TOKEN ?? env.GITHUB_TOKEN ?? "",
    responseFile: "",
    required: [],
    help: false,
  };

  const takeValue = (flag, value) => {
    if (value === undefined || value.startsWith("--")) {
      throw new UsageError(`${flag} needs a value`);
    }
    return value;
  };

  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i];
    switch (token) {
      case "--repo":
        options.repo = takeValue(token, argv[++i]);
        break;
      case "--tag":
        options.tag = takeValue(token, argv[++i]);
        break;
      case "--dist":
        options.dist = takeValue(token, argv[++i]);
        break;
      case "--require":
        options.required.push(takeValue(token, argv[++i]));
        break;
      case "--api-base":
        options.apiBase = takeValue(token, argv[++i]);
        break;
      case "--response-file":
        options.responseFile = takeValue(token, argv[++i]);
        break;
      case "--help":
      case "-h":
        options.help = true;
        break;
      default:
        throw new UsageError(`unknown argument: ${token}`);
    }
  }

  return options;
}

const USAGE = `Usage: node scripts/verify-release-assets.mjs [options]

  --repo <owner/name>     Repository holding the Release (GITHUB_REPOSITORY)
  --tag <vX.Y.Z>          Release tag to verify (GITHUB_REF_NAME)
  --dist <dir>            electron-builder output dir to read local artifacts from
  --require <asset-name>  Asset that must exist on the Release (repeatable)
  --response-file <path>  Read a captured Release API payload instead of calling GitHub
  --api-base <url>        GitHub API base URL (default https://api.github.com)
`;

export async function main(argv = process.argv.slice(2), env = process.env) {
  const options = parseArgs(argv, env);
  if (options.help) {
    process.stdout.write(USAGE);
    return 0;
  }

  const distDir = resolve(options.dist);
  const artifacts = collectArtifacts(distDir);
  const release = options.responseFile
    ? JSON.parse(readFileSync(options.responseFile, "utf-8"))
    : await fetchRelease(options);

  const label = options.responseFile
    ? `captured payload ${options.responseFile}`
    : `release ${options.repo}@${options.tag}`;
  console.log(
    `[verify-release-assets] ${artifacts.length} local artifact(s) in ${distDir}, checking against ${label}`,
  );

  const problems = evaluateRelease({
    artifacts,
    release,
    required: options.required,
    tag: options.tag || release?.tag_name || "<unknown>",
  });

  const uploaded = new Set(
    (Array.isArray(release?.assets) ? release.assets : []).map(
      (asset) => asset.name,
    ),
  );
  for (const artifact of artifacts) {
    if (uploaded.has(artifact.name)) {
      console.log(`[verify-release-assets] ok ${artifact.name}`);
    }
  }

  if (problems.length > 0) {
    console.error(
      `[verify-release-assets] FAILED: ${problems.length} problem(s) with ${label}`,
    );
    for (const problem of problems) {
      console.error(`  - ${problem}`);
    }
    return 1;
  }

  console.log(
    `[verify-release-assets] verified ${artifacts.length} artifact(s) on ${label}`,
  );
  return 0;
}

// Only run when invoked as a CLI, not when imported by a test file.
if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  main()
    .then((code) => {
      process.exitCode = code;
    })
    .catch((error) => {
      console.error(
        `[verify-release-assets] ${error instanceof UsageError ? "usage" : "error"}: ${error.message}`,
      );
      process.exitCode = 2;
    });
}
