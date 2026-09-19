import type {
  WorktreeCleanupItem,
  WorktreeCleanupReport,
} from "../../../main/worktree-cleanup";

/**
 * The reading half of the storage screen (DENE-617): what the list shows, in
 * what order, and what the totals say.
 *
 * Separate from the component because none of it needs a DOM, and because the
 * question it answers — "what will cleanup do to my disk, and to which copies"
 * — is one a user makes a decision on. A rule that decides that should be
 * readable as a table, not inferred from a render.
 *
 * Nothing here decides whether a copy may be removed. That verdict arrives
 * from the daemon, which is the only party that can see the filesystem, and is
 * re-checked there at the moment of removal.
 */

export type WorktreeKeepReason = NonNullable<WorktreeCleanupItem["keep_reason"]>;

/** A copy the policy would remove. */
export function isRemovable(item: WorktreeCleanupItem): boolean {
  return !item.keep_reason;
}

/**
 * List order: what cleanup would remove first, then what it is keeping.
 *
 * Removable copies lead because they are the answer to "what is this costing
 * me" — and within each group the largest first, since that is the one a user
 * looking to free space acts on. Path breaks ties so the order is stable
 * across refreshes rather than shifting under a click.
 */
export function sortForDisplay(items: WorktreeCleanupItem[]): WorktreeCleanupItem[] {
  return [...items].sort((a, b) => {
    const removable = Number(isRemovable(b)) - Number(isRemovable(a));
    if (removable !== 0) return removable;
    if (b.size_bytes !== a.size_bytes) return b.size_bytes - a.size_bytes;
    return a.path.localeCompare(b.path);
  });
}

export interface WorktreeCleanupTotals {
  /** Every working copy this machine holds. */
  count: number;
  bytes: number;
  /** The subset the policy would remove, and what that would reclaim. */
  removableCount: number;
  removableBytes: number;
}

export function totalsOf(items: WorktreeCleanupItem[]): WorktreeCleanupTotals {
  return items.reduce<WorktreeCleanupTotals>(
    (acc, item) => {
      acc.count += 1;
      acc.bytes += item.size_bytes;
      if (isRemovable(item)) {
        acc.removableCount += 1;
        acc.removableBytes += item.size_bytes;
      }
      return acc;
    },
    { count: 0, bytes: 0, removableCount: 0, removableBytes: 0 },
  );
}

/**
 * Whether the preview describes something that WILL happen or something that
 * would happen if the switch were on.
 *
 * The distinction is the whole reason cleanup can default to off and still be
 * trustworthy: the same list is computed either way, and only this flag says
 * whether anything acts on it.
 */
export function previewIsAdvisory(report: WorktreeCleanupReport): boolean {
  return report.settings.enabled !== true;
}

/** Human size, two significant-ish digits. A copy is megabytes at least. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 MB";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

/**
 * Days since a copy's last run, or null when it never recorded one.
 *
 * Null is not zero. A copy with no recorded run is one the age rule keeps, and
 * rendering it as "0 days ago" would read as the newest thing in the list.
 */
export function daysSince(timestamp: string, now: Date): number | null {
  const at = new Date(timestamp);
  if (Number.isNaN(at.getTime()) || at.getTime() <= 0) return null;
  const ms = now.getTime() - at.getTime();
  if (ms < 0) return 0;
  return Math.floor(ms / 86_400_000);
}
