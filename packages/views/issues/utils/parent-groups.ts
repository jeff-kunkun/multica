/**
 * Board parent groups (DENE-2760, `office_parent_groups`): inside a column, the
 * sub-issues of one parent sit together under one header, and every parent
 * keeps one color in every column. Display only — nothing here writes an issue.
 */

/** Validated categorical slots (light, dark). Wrapped in `light-dark()`, so
 *  the theme's `color-scheme` picks the variant. */
const PARENT_GROUP_PALETTE: ReadonlyArray<readonly [string, string]> = [
  ["#2a78d6", "#3987e5"], // blue
  ["#eb6834", "#d95926"], // orange
  ["#1baf7a", "#199e70"], // aqua
  ["#eda100", "#c98500"], // yellow
  ["#e87ba4", "#d55181"], // magenta
  ["#008300", "#008300"], // green
  ["#6250d6", "#9085e9"], // violet
  ["#e34948", "#e66767"], // red
];

export const PARENT_GROUP_COLORS = PARENT_GROUP_PALETTE.map(
  ([light, dark]) => `light-dark(${light}, ${dark})`,
);

/** A faint fill of `color` for chip backgrounds; the text stays foreground. */
export function parentGroupTint(color: string): string {
  return `color-mix(in srgb, ${color} 16%, transparent)`;
}

function homeSlot(parentId: string): number {
  // FNV-1a: the same id lands on the same slot in every column and session.
  let hash = 0x811c9dc5;
  for (let i = 0; i < parentId.length; i += 1) {
    hash ^= parentId.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0) % PARENT_GROUP_COLORS.length;
}

/**
 * One color per parent on the board. Each parent takes its hash slot, or the
 * next free one when an earlier parent already holds it, so the parents on
 * screen differ while there are no more of them than colors. Ids are UUIDv7,
 * so sorting is creation order: a parent that appears later takes a free slot
 * and never repaints the ones already shown.
 */
export function assignParentGroupColors(
  parentIds: Iterable<string>,
): Map<string, string> {
  const colors = new Map<string, string>();
  const taken = new Set<number>();
  const size = PARENT_GROUP_COLORS.length;
  for (const id of [...new Set(parentIds)].toSorted()) {
    const home = homeSlot(id);
    let slot = home;
    if (taken.size < size) {
      while (taken.has(slot)) slot = (slot + 1) % size;
    }
    taken.add(slot);
    colors.set(id, PARENT_GROUP_COLORS[slot]!);
  }
  return colors;
}

/**
 * Reorders one column so each parent's sub-issues are contiguous, at the slot
 * of that parent's first sub-issue. Cards without a parent keep their order
 * and slot. Idempotent, so the board can apply it to its drag mirror and the
 * column can apply it again while a drag has dropped a card mid-group.
 */
export function groupByParent<T>(
  items: readonly T[],
  parentOf: (item: T) => string | null | undefined,
): T[] {
  const members = new Map<string, T[]>();
  for (const item of items) {
    const parentId = parentOf(item);
    if (!parentId) continue;
    const group = members.get(parentId);
    if (group) group.push(item);
    else members.set(parentId, [item]);
  }
  if (members.size === 0) return [...items];
  const out: T[] = [];
  for (const item of items) {
    const parentId = parentOf(item);
    if (!parentId) {
      out.push(item);
      continue;
    }
    const group = members.get(parentId);
    if (!group) continue;
    out.push(...group);
    members.delete(parentId);
  }
  return out;
}

/** {@link groupByParent} over every column of a board's id mirror. */
export function groupColumnsByParent(
  columns: Record<string, string[]>,
  parentOf: (id: string) => string | null | undefined,
): Record<string, string[]> {
  return Object.fromEntries(
    Object.entries(columns).map(([columnId, ids]) => [columnId, groupByParent(ids, parentOf)]),
  );
}

export type ParentGroupRow<T> =
  | { kind: "card"; item: T; parentId: string | null }
  | { kind: "group"; parentId: string; count: number; collapsed: boolean };

/** Rows for one column: a header before each parent's sub-issues, which are
 *  left out while that header is collapsed. */
export function parentGroupRows<T>(
  items: readonly T[],
  parentOf: (item: T) => string | null | undefined,
  isCollapsed: (parentId: string) => boolean,
): ParentGroupRow<T>[] {
  const ordered = groupByParent(items, parentOf);
  const rows: ParentGroupRow<T>[] = [];
  let i = 0;
  while (i < ordered.length) {
    const parentId = parentOf(ordered[i]!) || null;
    if (!parentId) {
      rows.push({ kind: "card", item: ordered[i]!, parentId: null });
      i += 1;
      continue;
    }
    let end = i;
    while (end < ordered.length && parentOf(ordered[end]!) === parentId) end += 1;
    const collapsed = isCollapsed(parentId);
    rows.push({ kind: "group", parentId, count: end - i, collapsed });
    if (!collapsed) {
      for (let k = i; k < end; k += 1) {
        rows.push({ kind: "card", item: ordered[k]!, parentId });
      }
    }
    i = end;
  }
  return rows;
}

/** Key of one parent's group in one column, as stored in
 *  `boardCollapsedParentGroups`. */
export function parentGroupKey(columnId: string, parentId: string): string {
  return `${columnId}|${parentId}`;
}

/**
 * The parent's title without a leading "Project: " tag, which office titles
 * carry and which would make every header read the same. Kept when nothing
 * would be left after it, or when the colon is too far in to be a tag.
 */
export function shortParentTitle(title: string): string {
  const match = /^([^:：]{1,24})[:：]\s*(.+)$/.exec(title.trim());
  return match ? match[2]! : title.trim();
}
