// @vitest-environment node

import { describe, it, expect } from "vitest";
import type { WorktreeCleanupItem } from "../../../main/worktree-cleanup";
import {
  daysSince,
  formatBytes,
  isRemovable,
  previewIsAdvisory,
  sortForDisplay,
  totalsOf,
} from "./worktree-cleanup-view";

function item(over: Partial<WorktreeCleanupItem>): WorktreeCleanupItem {
  return {
    path: "/Users/me/code/app.multica-worktrees/task-1",
    git_root: "/Users/me/code/app",
    branch: "agent/j/task-1",
    multica_created: true,
    in_use: false,
    last_run_at: "2026-08-01T00:00:00Z",
    dirty: false,
    merged: true,
    unknown: false,
    size_bytes: 1024,
    ...over,
  };
}

describe("worktree cleanup view", () => {
  it("treats a copy with no keep reason as removable", () => {
    expect(isRemovable(item({}))).toBe(true);
    expect(isRemovable(item({ keep_reason: "uncommitted_changes" }))).toBe(false);
  });

  // Removable first (the answer to "what is this costing me"), largest first
  // within a group, path as the tie-break so the order does not shift under a
  // click.
  it("lists removable copies first, then largest, then by path", () => {
    const keptSmall = item({ path: "/a", size_bytes: 10, keep_reason: "in_use" });
    const keptBig = item({ path: "/b", size_bytes: 900, keep_reason: "in_use" });
    const removableSmall = item({ path: "/c", size_bytes: 20 });
    const removableBig = item({ path: "/d", size_bytes: 800 });
    const tieA = item({ path: "/e", size_bytes: 800 });

    const order = sortForDisplay([keptSmall, removableSmall, keptBig, tieA, removableBig]).map(
      (i) => i.path,
    );
    expect(order).toEqual(["/d", "/e", "/c", "/b", "/a"]);
  });

  it("totals the whole list and the removable subset separately", () => {
    const totals = totalsOf([
      item({ path: "/a", size_bytes: 100 }),
      item({ path: "/b", size_bytes: 200, keep_reason: "branch_not_merged" }),
      item({ path: "/c", size_bytes: 300 }),
    ]);
    expect(totals).toEqual({
      count: 3,
      bytes: 600,
      removableCount: 2,
      removableBytes: 400,
    });
  });

  // Invariant 6: the preview is computed whether cleanup is on or off, and
  // only this flag says whether anything acts on it.
  it("marks the preview advisory exactly when cleanup is switched off", () => {
    expect(previewIsAdvisory({ settings: { enabled: false }, items: [] })).toBe(true);
    expect(previewIsAdvisory({ settings: { enabled: true }, items: [] })).toBe(false);
  });

  it("formats sizes at a readable scale", () => {
    expect(formatBytes(0)).toBe("0 MB");
    expect(formatBytes(-5)).toBe("0 MB");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(3.3 * 1024 ** 3)).toBe("3.3 GB");
    expect(formatBytes(20 * 1024 ** 3)).toBe("20 GB");
  });

  // A copy that never recorded a run is not "0 days ago" — that would read as
  // the newest thing in the list, when it is the one the age rule keeps.
  it("reports an unrecorded last run as unknown rather than today", () => {
    const now = new Date("2026-09-19T12:00:00Z");
    expect(daysSince("2026-09-05T12:00:00Z", now)).toBe(14);
    expect(daysSince("", now)).toBeNull();
    expect(daysSince("0001-01-01T00:00:00Z", now)).toBeNull();
    // A clock skew into the future is today, not a negative age.
    expect(daysSince("2026-09-20T12:00:00Z", now)).toBe(0);
  });
});
