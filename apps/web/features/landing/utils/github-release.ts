import {
  hasAllPlatformDownloads,
  parseReleaseAssets,
  type DownloadAssets,
} from "./parse-release-assets";

/**
 * Server-side fetcher for the latest downloadable Multica release,
 * designed to run inside a Next.js server component. Response is cached
 * by the Next.js fetch cache for 5 minutes (Vercel ISR) so hitting
 * /download costs at most one GitHub API call per region per 5 minutes.
 *
 * Desktop assets don't all land at the same time: the macOS, Linux and
 * Windows packaging jobs run in parallel on separate runners and finish
 * minutes apart, and any one of them can fail outright and leave a
 * release permanently short of a platform. Either way the newest release
 * is not always the newest *downloadable* one, so we pull a short window
 * of recent releases and show the newest that covers all three platforms
 * — nobody then lands on a page with no build for the machine they are
 * reading it on.
 *
 * On any failure (network, rate limit, malformed payload) returns a
 * `null`-shaped result and logs — the page degrades to a "version
 * unavailable" view rather than 500ing.
 */

export interface LatestRelease {
  version: string | null;
  publishedAt: string | null;
  htmlUrl: string | null;
  assets: DownloadAssets;
  /**
   * The releases index of the repo this release was read from — the
   * escape hatch /download links to when an asset is missing or the API
   * failed. Carried on the payload rather than hardcoded in the client
   * because the repo is deployment-configurable (see `releasesRepo`),
   * and a fallback link pointing at a different repo than the buttons is
   * how someone ends up downloading a build the page never offered.
   */
  releasesUrl: string;
}

// Five candidates tolerates four consecutive incomplete releases before
// the page has to fall back to showing the newest one as-is. Releases
// ship roughly daily, so that is days of head room — while staying one
// cheap request.
const CANDIDATE_COUNT = 5;

// Where /download reads releases from. This deployment ships from the
// kun fork, which is where its desktop installers are actually built and
// uploaded; pointing at the upstream repo would advertise versions whose
// assets this instance never publishes. `GITHUB_RELEASE_REPO` overrides
// it for a deployment that releases from somewhere else. Server-side
// only — never prefix it with `NEXT_PUBLIC_`.
const DEFAULT_RELEASE_REPO = "jeff-kunkun/multica";

const REPO_SLUG_RE = /^[\w.-]+\/[\w.-]+$/;

/**
 * The `owner/name` slug to read releases from. Read per call rather than
 * frozen at module load so the value follows the running process's env
 * (Next.js evaluates this module once per server, long before a
 * self-hosted container's env is necessarily interesting to it).
 *
 * A malformed override falls back to the default instead of being
 * interpolated into the API URL: a slug containing a slash or a `..`
 * would silently retarget the request at a different GitHub endpoint.
 */
export function releasesRepo(): string {
  const configured = process.env.GITHUB_RELEASE_REPO?.trim();
  if (!configured) return DEFAULT_RELEASE_REPO;
  if (!REPO_SLUG_RE.test(configured)) {
    console.warn(
      `[download] ignoring malformed GITHUB_RELEASE_REPO '${configured}';` +
        ` expected owner/name — falling back to ${DEFAULT_RELEASE_REPO}`,
    );
    return DEFAULT_RELEASE_REPO;
  }
  return configured;
}

/** The repo's human-facing releases index — /download's escape hatch. */
export function releasesPageUrl(repo = releasesRepo()): string {
  return `https://github.com/${repo}/releases`;
}

const REVALIDATE_SECONDS = 300;

interface GitHubReleasePayload {
  tag_name?: string;
  published_at?: string;
  html_url?: string;
  prerelease?: boolean;
  draft?: boolean;
  assets?: Array<{ name: string; browser_download_url: string }>;
}

export async function fetchLatestRelease(): Promise<LatestRelease> {
  const headers: Record<string, string> = {
    Accept: "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
  };
  // Optional PAT for local development and self-hosted deploys where
  // the shared outbound IP keeps hitting the 60-requests/hour
  // unauthenticated limit. Vercel's fetch cache is shared across all
  // regions so production rarely needs this — but the env var lets
  // anyone running the site locally avoid the rate-limit dance. Never
  // prefix this with `NEXT_PUBLIC_`; the token must stay server-side.
  const token = process.env.GITHUB_TOKEN;
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  const repo = releasesRepo();
  const apiUrl = `https://api.github.com/repos/${repo}/releases?per_page=${CANDIDATE_COUNT}`;

  try {
    const res = await fetch(apiUrl, {
      next: { revalidate: REVALIDATE_SECONDS },
      headers,
    });
    if (!res.ok) {
      throw new Error(`GitHub API responded ${res.status}`);
    }
    const data = (await res.json()) as GitHubReleasePayload[];

    // Defensive filter — Multica doesn't publish prereleases or drafts
    // today, but the endpoint returns them if that ever changes. A
    // prerelease shadowing a stable version on /download would be a
    // regression.
    const stable = data.filter((r) => !r.prerelease && !r.draft);
    const chosen = pickRelease(stable);
    if (!chosen) {
      return emptyRelease(repo);
    }

    return {
      version: chosen.release.tag_name ?? null,
      publishedAt: chosen.release.published_at ?? null,
      htmlUrl: chosen.release.html_url ?? null,
      assets: chosen.assets,
      releasesUrl: releasesPageUrl(repo),
    };
  } catch (err) {
    console.warn("[download] fetchLatestRelease failed:", err);
    return emptyRelease(repo);
  }
}

interface PickedRelease {
  release: GitHubReleasePayload;
  assets: DownloadAssets;
}

/**
 * Newest release that carries a build for every platform. Falls back to
 * the newest release overall when no candidate does — the page then
 * shows what does exist plus its "all releases" escape hatch, which
 * still beats reporting no version at all.
 */
function pickRelease(
  candidates: GitHubReleasePayload[],
): PickedRelease | undefined {
  const parsed = candidates.map((release) => ({
    release,
    assets: parseReleaseAssets(release.assets ?? []),
  }));

  const complete = parsed.find((c) => hasAllPlatformDownloads(c.assets));
  if (complete) {
    if (complete !== parsed[0]) {
      // Worth a line in the logs: a release that never completes its
      // asset set is invisible on /download until someone notices.
      console.warn(
        `[download] skipping ${parsed[0]?.release.tag_name ?? "latest release"}` +
          ` — no desktop build for some platform; showing ${complete.release.tag_name}`,
      );
    }
    return complete;
  }
  return parsed[0];
}

function emptyRelease(repo = releasesRepo()): LatestRelease {
  return {
    version: null,
    publishedAt: null,
    htmlUrl: null,
    assets: {},
    releasesUrl: releasesPageUrl(repo),
  };
}
