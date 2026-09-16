import type { Agent } from "@multica/core/types";
import { isSpecialization, specializationCount } from "../specialization";

/** Action row height for "derive a specialisation" — the same rhythm as a
 *  group header (`h-9`): a full-width affordance, not a data row. */
export const SPECIALIZATION_DERIVE_ROW_HEIGHT = 36;

export interface AgentSpecializationGroup<T extends { agent: Agent }> {
  /** The base role this group hangs off. */
  base: T;
  /** Specialisations of `base` present in the filtered row set, in row order. */
  children: T[];
  /**
   * Server-reported count of active specialisations, which can exceed
   * `children.length` when a filter (or the scope) hides some of them.
   */
  count: number;
}

/**
 * Rows laid out for the nested "group by base role" view, in row order.
 *
 * A specialisation is rendered under its base role only when that base role is
 * itself visible after filtering. Otherwise it stays where it was, as a
 * standalone row: a filtered or scoped view must never make an agent disappear
 * just because its parent did (the "flat fallback" of the design).
 */
export type AgentSpecializationVirtualItem<T extends { agent: Agent }> =
  | {
      kind: "base";
      key: string;
      row: T;
      groupId: string;
      /** Specialisations hanging off this row (0 for a specialisation). */
      childCount: number;
      expanded: boolean;
    }
  | {
      kind: "specialization";
      key: string;
      row: T;
      groupId: string;
      /** Name of the base role, shown as the row's relationship label. */
      parentName: string;
    }
  | { kind: "derive"; key: string; groupId: string; base: T };

/**
 * One group per row, in row order: a base role carries the specialisations
 * that are visible alongside it, a specialisation whose base role was filtered
 * away carries none (and renders flat).
 */
export function groupRowsByBaseRole<T extends { agent: Agent }>(
  rows: readonly T[],
): AgentSpecializationGroup<T>[] {
  const visible = new Set(rows.map((row) => row.agent.id));
  const groups: AgentSpecializationGroup<T>[] = [];
  const claimed = new Set<string>();
  for (const row of rows) {
    const parentId = row.agent.parent_agent_id ?? "";
    // Claimed by its base role's group already.
    if (isSpecialization(row.agent) && visible.has(parentId)) continue;
    if (claimed.has(row.agent.id)) continue;
    claimed.add(row.agent.id);
    const children =
      parentId === ""
        ? rows.filter((candidate) => candidate.agent.parent_agent_id === row.agent.id)
        : [];
    for (const child of children) claimed.add(child.agent.id);
    groups.push({
      base: row,
      children,
      count: specializationCount(row.agent, children.length),
    });
  }
  return groups;
}

/**
 * Flattens the nested view for the virtualizer.
 *
 * A collapsed base role emits only its own row — the count chip on that row is
 * what says a group is folded, so no extra summary line is needed. An expanded
 * one ends with the derive entry: the only way to create a specialisation that
 * already points at this base role.
 */
export function flattenSpecializationItems<T extends { agent: Agent }>(
  rows: readonly T[],
  collapsed: ReadonlySet<string>,
): AgentSpecializationVirtualItem<T>[] {
  const items: AgentSpecializationVirtualItem<T>[] = [];
  for (const group of groupRowsByBaseRole(rows)) {
    const groupId = group.base.agent.id;
    const isBase = !isSpecialization(group.base.agent);
    const expanded = isBase && !collapsed.has(groupId);
    items.push({
      kind: "base",
      key: groupId,
      row: group.base,
      groupId,
      childCount: group.count,
      expanded,
    });
    if (!expanded) continue;
    for (const child of group.children) {
      items.push({
        kind: "specialization",
        key: `${groupId}:${child.agent.id}`,
        row: child,
        groupId,
        parentName: group.base.agent.name,
      });
    }
    items.push({
      kind: "derive",
      key: `${groupId}:derive`,
      groupId,
      base: group.base,
    });
  }
  return items;
}
