// The Updates tab lists what the client, the daemon and the server run, side
// by side. It does not judge them: Kun (2026-10-29) dropped the red/green
// verdict, so a side that differs or has no commit is shown like any other.

export type VersionSide = "client" | "daemon" | "server";

export interface SideVersion {
  side: VersionSide;
  /** What the side reported, shown as-is; empty when it reported nothing. */
  reported: string;
  /** Commit read from what it reported; empty for "dev" and untagged builds. */
  commit: string;
}

// A commit is 7–40 hex characters standing alone or between version
// separators, with at least one letter so a date or build number never counts.
const COMMIT_IN = /(?:^|[.\-+_])([0-9a-f]{7,40})(?=$|[.\-+_])/g;

export function commitOf(text: string | undefined): string {
  const value = (text ?? "").trim().toLowerCase();
  for (const found of value.matchAll(COMMIT_IN)) {
    const hex = found[1] ?? "";
    if (/[a-f]/.test(hex)) return hex;
  }
  return "";
}

export function sideVersions(input: {
  client?: string;
  daemonVersion?: string;
  daemonCommit?: string;
  server?: string;
}): SideVersion[] {
  const daemonReported = input.daemonCommit?.trim() || input.daemonVersion?.trim() || "";
  return [
    { side: "client", reported: input.client?.trim() ?? "", commit: commitOf(input.client) },
    {
      side: "daemon",
      reported: daemonReported,
      commit: commitOf(input.daemonCommit) || commitOf(input.daemonVersion),
    },
    { side: "server", reported: input.server?.trim() ?? "", commit: commitOf(input.server) },
  ];
}
