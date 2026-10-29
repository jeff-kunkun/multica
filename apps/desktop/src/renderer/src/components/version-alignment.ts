// Client, daemon and server line up only when all three carry the same real
// commit (DENE-2756). The rule matches the status light's 三边版本 row
// (status-light collect/collect.py), so the Updates tab and the light agree.

export type VersionSide = "client" | "daemon" | "server";

export type SideState = "match" | "mismatch" | "no_commit";

export interface SideVersion {
  side: VersionSide;
  /** What the side reported, shown as-is; empty when it reported nothing. */
  reported: string;
  /** Commit read from what it reported; empty for "dev" and untagged builds. */
  commit: string;
  state: SideState;
}

export interface VersionAlignment {
  sides: SideVersion[];
  aligned: boolean;
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

/** A short and a full hash of the same commit are the same commit. */
export function sameCommit(a: string, b: string): boolean {
  return Boolean(a && b) && (a.startsWith(b) || b.startsWith(a));
}

export function alignVersions(input: {
  client?: string;
  daemonVersion?: string;
  daemonCommit?: string;
  server?: string;
}): VersionAlignment {
  const daemonReported = input.daemonCommit?.trim() || input.daemonVersion?.trim() || "";
  const reported: Array<[VersionSide, string, string]> = [
    ["client", input.client?.trim() ?? "", commitOf(input.client)],
    [
      "daemon",
      daemonReported,
      commitOf(input.daemonCommit) || commitOf(input.daemonVersion),
    ],
    ["server", input.server?.trim() ?? "", commitOf(input.server)],
  ];
  const commits = reported.map(([, , commit]) => commit).filter(Boolean);
  // With three sides, two that agree are the majority and the odd one out is
  // red. A lone commit has nothing to disagree with; sides without one are red.
  const reference =
    commits.length === 1
      ? (commits[0] ?? "")
      : (commits.find((a) => commits.filter((b) => sameCommit(a, b)).length >= 2) ?? "");
  const sides = reported.map(([side, value, commit]): SideVersion => ({
    side,
    reported: value,
    commit,
    state: !commit ? "no_commit" : sameCommit(commit, reference) ? "match" : "mismatch",
  }));
  return { sides, aligned: sides.every((s) => s.state === "match") };
}
