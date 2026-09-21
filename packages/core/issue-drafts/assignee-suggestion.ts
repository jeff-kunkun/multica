import type { Agent, IssueDraftPayload, Squad } from "../types";

/**
 * Suggested assignee for one alignment node. `null` means leave the row
 * unassigned — the roster had nothing unique to match, or was unavailable.
 */
export interface IssueDraftSuggestedAssignee {
  assigneeType: "agent";
  assigneeId: string;
}

export interface IssueDraftAssigneeSuggestions {
  parent: IssueDraftSuggestedAssignee | null;
  children: Record<string, IssueDraftSuggestedAssignee | null>;
}

/**
 * One assignable seat, tagged with what the workspace already wrote on the
 * roster: squad role, routing tier, name pieces, and direction squad names.
 * Matching never invents a parallel assignment table; it only scores a hint
 * against these tags.
 */
export interface IssueDraftAssigneeRosterEntry {
  assigneeId: string;
  tags: string[];
  /** Empty/absent when this seat is a base role rather than a specialisation. */
  parentAgentId?: string | null;
}

const GENERIC_ROLES = new Set(["", "member", "leader"]);

const TIER_LABELS: Record<string, readonly string[]> = {
  strongest: ["strongest", "最强"],
  strong: ["strong", "强"],
  medium: ["medium", "中"],
  weak: ["weak", "弱"],
};

/**
 * Expands the work-kind words the carrier is told to write (see
 * `issueDraftContract`) onto tokens that already appear on roster tags.
 * This is matching vocabulary, not a per-workspace assignment config.
 */
const HINT_ALIASES: Record<string, readonly string[]> = {
  backend: ["builder", "实现"],
  后端: ["builder", "实现"],
  implementation: ["builder", "实现"],
  实现: ["builder", "实现"],
  builder: ["builder", "实现"],
  frontend: ["builder", "实现", "前端"],
  前端: ["builder", "实现", "前端"],
  page: ["页面", "网页"],
  页面: ["页面", "网页"],
  ui: ["builder", "实现", "前端"],
  verification: ["operator", "验收"],
  验收: ["operator", "验收"],
  验证: ["operator", "验收"],
  qa: ["operator", "验收"],
  operator: ["operator", "验收"],
  manual: ["operator", "验收"],
  architecture: ["architect", "架构"],
  架构: ["architect", "架构"],
  architect: ["architect", "架构"],
  review: ["review", "审查"],
  审查: ["review", "审查"],
  reviewer: ["review", "审查"],
};

const DIRECTION_TOKENS = [
  "游戏",
  "出海",
  "自媒体",
  "学术",
  "game",
  "overseas",
  "academic",
] as const;

const TOKEN_SPLIT = /[\s,;|/\\()（）[\]【】{}<>+\-_:：、。·]+/;

function tokenize(value: string): string[] {
  return value
    .toLowerCase()
    .split(TOKEN_SPLIT)
    .map((token) => token.trim())
    .filter((token) => token.length > 0);
}

function addCJKPieces(value: string, into: Set<string>): void {
  for (const part of value.split(/[与和的]/)) {
    const trimmed = part.trim();
    if (trimmed.length >= 2) into.add(trimmed.toLowerCase());
  }
}

function collectTags(values: readonly string[]): string[] {
  const tags = new Set<string>();
  for (const value of values) {
    const trimmed = value.trim();
    if (!trimmed) continue;
    tags.add(trimmed.toLowerCase());
    for (const token of tokenize(trimmed)) tags.add(token);
    addCJKPieces(trimmed, tags);
  }
  for (const tag of [...tags]) {
    for (const direction of DIRECTION_TOKENS) {
      const needle = direction.toLowerCase();
      if (tag === needle || tag.includes(needle)) tags.add(needle);
    }
  }
  return [...tags];
}

function expandHint(text: string): string[] {
  const tokens = tokenize(text);
  const expanded = new Set(tokens);
  for (const token of tokens) {
    for (const alias of HINT_ALIASES[token] ?? []) expanded.add(alias);
  }
  return [...expanded];
}

function directionTokensIn(text: string): string[] {
  const haystack = text.toLowerCase();
  return DIRECTION_TOKENS.filter((token) => haystack.includes(token.toLowerCase()));
}

function tokenHitsTag(token: string, tag: string): "exact" | "partial" | null {
  if (token === tag) return "exact";
  if (token.length < 2 || tag.length < 2) return null;
  return tag.includes(token) || token.includes(tag) ? "partial" : null;
}

function scoreEntry(
  entry: IssueDraftAssigneeRosterEntry,
  hintTokens: readonly string[],
  directionTokens: readonly string[],
): number {
  if (hintTokens.length === 0) return 0;
  const tags = entry.tags;
  let score = 0;
  for (const token of hintTokens) {
    let best: "exact" | "partial" | null = null;
    for (const tag of tags) {
      const hit = tokenHitsTag(token, tag);
      if (hit === "exact") {
        best = "exact";
        break;
      }
      if (hit === "partial") best = "partial";
    }
    if (best === "exact") score += 2;
    else if (best === "partial") score += 1;
  }
  for (const direction of directionTokens) {
    if (tags.some((tag) => tag === direction || tag.includes(direction))) {
      score += 3;
    }
  }
  return score;
}

function pickUnique(
  roster: readonly IssueDraftAssigneeRosterEntry[],
  hint: string,
  directionTokens: readonly string[],
): IssueDraftSuggestedAssignee | null {
  const tokens = expandHint(hint);
  if (tokens.length === 0) return null;
  const scored = roster
    .map((entry) => ({ entry, score: scoreEntry(entry, tokens, directionTokens) }))
    .filter((row) => row.score > 0)
    .sort((a, b) => b.score - a.score);
  if (scored.length === 0) return null;
  const best = scored[0]!.score;
  const top = scored.filter((row) => row.score === best);
  const winner = top.length === 1
    ? top[0]!.entry
    : uniqueBaseSeat(top.map((row) => row.entry));
  if (!winner) return null;
  return { assigneeType: "agent", assigneeId: winner.assigneeId };
}

function uniqueBaseSeat(
  entries: readonly IssueDraftAssigneeRosterEntry[],
): IssueDraftAssigneeRosterEntry | null {
  const bases = entries.filter((entry) => !entry.parentAgentId);
  return bases.length === 1 ? bases[0]! : null;
}

function rosterRolesByAgent(
  squads: readonly Squad[],
): Map<string, string[]> {
  const roles = new Map<string, string[]>();
  const add = (agentId: string, role: string) => {
    const current = roles.get(agentId);
    if (current) current.push(role);
    else roles.set(agentId, [role]);
  };
  for (const squad of squads) {
    if (squad.archived_at) continue;
    const members = squad.members ?? squad.member_preview ?? [];
    for (const member of members) {
      if (member.member_type !== "agent") continue;
      add(member.member_id, member.role ?? "");
    }
  }
  return roles;
}

function squadNamesByAgent(
  squads: readonly Squad[],
): Map<string, string[]> {
  const names = new Map<string, string[]>();
  for (const squad of squads) {
    if (squad.archived_at) continue;
    const members = squad.members ?? squad.member_preview ?? [];
    for (const member of members) {
      if (member.member_type !== "agent") continue;
      const current = names.get(member.member_id);
      if (current) current.push(squad.name);
      else names.set(member.member_id, [squad.name]);
    }
  }
  return names;
}

/**
 * Builds the matching roster from the workspace's existing agent list and
 * squad membership. Archived seats are dropped. Generic roles (`member`,
 * `leader`) are not tags; a specialisation inherits its parent's non-generic
 * roles so a direction seat can still match a work kind.
 */
export function buildIssueDraftAssigneeRoster(input: {
  agents: readonly Agent[];
  squads: readonly Squad[];
}): IssueDraftAssigneeRosterEntry[] {
  const rolesByAgent = rosterRolesByAgent(input.squads);
  const squadsByAgent = squadNamesByAgent(input.squads);

  const entries: IssueDraftAssigneeRosterEntry[] = [];
  for (const agent of input.agents) {
    if (agent.archived_at) continue;
    const ownRoles = (rolesByAgent.get(agent.id) ?? []).filter(
      (role) => !GENERIC_ROLES.has(role.trim().toLowerCase()),
    );
    const parentId = agent.parent_agent_id || null;
    const inheritedRoles = parentId
      ? (rolesByAgent.get(parentId) ?? []).filter(
          (role) => !GENERIC_ROLES.has(role.trim().toLowerCase()),
        )
      : [];
    const tier = agent.routing_tier?.trim().toLowerCase() ?? "";
    const tierLabels = TIER_LABELS[tier] ?? (tier ? [tier] : []);
    const tags = collectTags([
      ...ownRoles,
      ...inheritedRoles,
      ...tierLabels,
      agent.name,
      agent.parent_agent_name ?? "",
      ...(squadsByAgent.get(agent.id) ?? []),
    ]);
    entries.push({
      assigneeId: agent.id,
      tags,
      parentAgentId: parentId,
    });
  }
  return entries;
}

/**
 * Suggests an assignee for the parent and every child. Empty hint / empty
 * roster / no unique winner all return `null` (unassigned). The parent uses
 * its title and description as the hint; children use `assignee_hint`, then
 * the child title.
 */
export function suggestIssueDraftAssignees(
  payload: IssueDraftPayload,
  roster: readonly IssueDraftAssigneeRosterEntry[],
): IssueDraftAssigneeSuggestions {
  if (roster.length === 0) {
    return {
      parent: null,
      children: Object.fromEntries(
        (payload.children ?? []).map((child) => [child.key, null]),
      ),
    };
  }
  const directionSource = [
    payload.title,
    payload.description,
    ...(payload.children ?? []).map((child) => child.assignee_hint ?? ""),
    ...(payload.children ?? []).map((child) => child.title),
  ].join("\n");
  const directions = directionTokensIn(directionSource);
  const parentHint = `${payload.title} ${payload.description}`.trim();
  const children: Record<string, IssueDraftSuggestedAssignee | null> = {};
  for (const child of payload.children ?? []) {
    const hint = (child.assignee_hint ?? "").trim() || child.title;
    children[child.key] = pickUnique(roster, hint, directions);
  }
  return {
    parent: pickUnique(roster, parentHint, directions),
    children,
  };
}

/**
 * Fills empty assignee slots from suggestions. Already-set slots, skipped
 * keys (user-cleared or already-built nodes), and `null` suggestions are
 * left untouched. Returns the original payload when nothing changed.
 */
export function applyIssueDraftAssigneeSuggestions(
  payload: IssueDraftPayload,
  suggestions: IssueDraftAssigneeSuggestions,
  options?: { skipKeys?: ReadonlySet<string> },
): IssueDraftPayload {
  const skip = options?.skipKeys;
  let changed = false;
  let assigneeType = payload.assignee_type ?? null;
  let assigneeId = payload.assignee_id ?? null;
  if (
    !assigneeId &&
    suggestions.parent &&
    skip?.has("") !== true
  ) {
    assigneeType = suggestions.parent.assigneeType;
    assigneeId = suggestions.parent.assigneeId;
    changed = true;
  }
  const children = payload.children ?? [];
  const nextChildren = children.map((child) => {
    if (child.assignee_id || skip?.has(child.key)) return child;
    const suggestion = suggestions.children[child.key];
    if (!suggestion) return child;
    changed = true;
    return {
      ...child,
      assignee_type: suggestion.assigneeType,
      assignee_id: suggestion.assigneeId,
    };
  });
  if (!changed) return payload;
  return {
    ...payload,
    assignee_type: assigneeType,
    assignee_id: assigneeId,
    ...(children.length > 0 || nextChildren.length > 0
      ? { children: nextChildren }
      : {}),
  };
}
