import type { Squad, SquadMemberPreview } from "@multica/core/types";

/** Filter / group sentinel for agents that belong to no active squad. */
export const NO_SQUAD_ID = "__none__";

/** Group title row height — matches `h-9` on the list column header. */
export const GROUP_HEADER_HEIGHT = 36;

function squadMembers(squad: Squad): SquadMemberPreview[] {
  return squad.members ?? squad.member_preview ?? [];
}

export function isActiveSquad(squad: Squad): boolean {
  return squad.archived_at == null;
}

export function squadAgentMemberIds(squad: Squad): string[] {
  const ids: string[] = [];
  for (const member of squadMembers(squad)) {
    if (member.member_type === "agent") ids.push(member.member_id);
  }
  return ids;
}

/** Active squads that contain a given agent, in list order. */
export function buildAgentSquadsMap(
  squads: readonly Squad[],
): Map<string, Squad[]> {
  const map = new Map<string, Squad[]>();
  for (const squad of squads) {
    if (!isActiveSquad(squad)) continue;
    for (const agentId of squadAgentMemberIds(squad)) {
      const existing = map.get(agentId);
      if (existing) existing.push(squad);
      else map.set(agentId, [squad]);
    }
  }
  return map;
}

/**
 * Squad filter is OR across selected ids, including `__none__` for
 * unassigned agents. An empty selection is inactive (the row passes).
 */
export function rowMatchesSquadFilter(
  squadIds: readonly string[],
  selected: readonly string[],
): boolean {
  if (selected.length === 0) return true;
  const unassigned = squadIds.length === 0;
  for (const value of selected) {
    if (value === NO_SQUAD_ID) {
      if (unassigned) return true;
      continue;
    }
    if (squadIds.includes(value)) return true;
  }
  return false;
}

export interface AgentSquadGroup<
  T extends { agent: { id: string }; squadIds: readonly string[] },
> {
  id: string;
  squad: Squad | null;
  rows: T[];
}

/**
 * One group per active squad that still has a matching agent after filters,
 * then a trailing "no squad" group for unassigned agents. Multi-squad agents
 * appear in every matching group. Row order inside a group follows `rows`.
 */
export function groupRowsBySquad<
  T extends { agent: { id: string }; squadIds: readonly string[] },
>(rows: readonly T[], squads: readonly Squad[]): AgentSquadGroup<T>[] {
  const groups: AgentSquadGroup<T>[] = [];
  const active = squads
    .filter(isActiveSquad)
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name));

  for (const squad of active) {
    const memberIds = new Set(squadAgentMemberIds(squad));
    const memberRows = rows.filter((row) => memberIds.has(row.agent.id));
    if (memberRows.length === 0) continue;
    groups.push({ id: squad.id, squad, rows: memberRows });
  }

  const unassigned = rows.filter((row) => row.squadIds.length === 0);
  if (unassigned.length > 0) {
    groups.push({ id: NO_SQUAD_ID, squad: null, rows: [...unassigned] });
  }
  return groups;
}

export type AgentListVirtualItem<
  T extends { agent: { id: string }; squadIds: readonly string[] },
> =
  | { kind: "header"; key: string; group: AgentSquadGroup<T> }
  | { kind: "row"; key: string; row: T; groupId: string };

export function flattenAgentListItems<
  T extends { agent: { id: string }; squadIds: readonly string[] },
>(
  rows: readonly T[],
  squads: readonly Squad[],
  grouping: "none" | "squad",
): AgentListVirtualItem<T>[] {
  if (grouping !== "squad") {
    return rows.map((row) => ({
      kind: "row" as const,
      key: row.agent.id,
      row,
      groupId: "",
    }));
  }
  const items: AgentListVirtualItem<T>[] = [];
  for (const group of groupRowsBySquad(rows, squads)) {
    items.push({ kind: "header", key: `header:${group.id}`, group });
    for (const row of group.rows) {
      items.push({
        kind: "row",
        key: `${group.id}:${row.agent.id}`,
        row,
        groupId: group.id,
      });
    }
  }
  return items;
}
