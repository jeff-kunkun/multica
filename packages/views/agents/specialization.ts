import type { Agent, AgentSkillSummary } from "@multica/core/types";
import { errorCode } from "@multica/core/api";

/**
 * Two-level specialisation (DENE-301), as the client sees it.
 *
 * A base role has no `parent_agent_id`; a specialisation points at exactly one
 * base role and can never be a parent itself (the server enforces the depth).
 * Every helper here reads the SAME fields the server serves — no client-side
 * inference of the relationship beyond "non-empty id means attached", which is
 * also how the backend models it (`parent_agent_id IS NULL` = base role).
 *
 * Older backends omit all of these fields. `undefined` must therefore behave
 * exactly like the empty string / empty list: the flat list the product had
 * before this feature, never a broken tree.
 */

/** The separator the backend joins a parent's prompt with — keep in sync with
 *  `agentInstructionSeparator` in `server/internal/handler/agent.go`. */
export const SPECIALIZATION_INSTRUCTION_SEPARATOR = "\n\n";

export function isSpecialization(
  agent: Pick<Agent, "parent_agent_id">,
): boolean {
  return (agent.parent_agent_id ?? "").length > 0;
}

export function isBaseRole(agent: Pick<Agent, "parent_agent_id">): boolean {
  return !isSpecialization(agent);
}

/**
 * Active specialisations of one base role, from an already-loaded list.
 *
 * The list carries `parent_agent_id` per row, so the nested view and the
 * archive guard never need a per-agent request. Archived children are included
 * — the caller decides; the archive guard's server-side counterpart counts
 * only active ones (`child_count`).
 */
export function childrenOf(
  agents: readonly Agent[],
  parentId: string,
): Agent[] {
  if (!parentId) return [];
  return agents.filter((agent) => agent.parent_agent_id === parentId);
}

/** Active children of one base role, in list order. */
export function activeChildrenOf(
  agents: readonly Agent[],
  parentId: string,
): Agent[] {
  return childrenOf(agents, parentId).filter((agent) => !agent.archived_at);
}

/**
 * How many specialisations a base-role row reports.
 *
 * `child_count` is the server's workspace-wide count of ACTIVE children, so it
 * stays right even when filters hide some of them; it is preferred over the
 * visible children the caller happens to hold. A backend that does not send it
 * still gets an honest number from the rows on screen.
 */
export function specializationCount(
  agent: Pick<Agent, "parent_agent_id" | "child_count">,
  visibleChildren: number,
): number {
  if (isSpecialization(agent)) return 0;
  const count = agent.child_count;
  if (typeof count === "number" && Number.isFinite(count) && count >= 0) {
    return count;
  }
  return visibleChildren;
}

/**
 * Agents a new specialisation may be attached to: base roles only (the server
 * refuses a parent that is itself a specialisation), never archived ones (that
 * refusal is a 400), sorted the way every other agent picker sorts.
 */
export function baseRoleOptions(agents: readonly Agent[]): Agent[] {
  return agents
    .filter((agent) => isBaseRole(agent) && !agent.archived_at)
    .toSorted((a, b) => a.name.localeCompare(b.name));
}

/**
 * The prompt a specialisation actually runs with: the base role's prompt, a
 * blank line, then its own. Mirrors `composeAgentInstructions` on the server,
 * including the "no stray blank lines when one side is empty" rule — the
 * preview is only useful if it shows exactly what the daemon is handed.
 */
export function composeEffectiveInstructions(
  inherited: string | undefined,
  own: string | undefined,
): string {
  const parent = inherited ?? "";
  const child = own ?? "";
  if (parent === "") return child;
  if (child === "") return parent;
  return `${parent}${SPECIALIZATION_INSTRUCTION_SEPARATOR}${child}`;
}

/**
 * Inherited skills that the child does not also hold itself. The server serves
 * the parent's list verbatim, so the overlap is expected when both attached
 * the same skill; the chip row must not show it twice (v1 cannot remove an
 * inherited binding, so a duplicate would read as an unremovable twin).
 */
export function inheritedSkillChips(
  agent: Pick<Agent, "inherited_skills" | "skills">,
): AgentSkillSummary[] {
  const inherited = agent.inherited_skills ?? [];
  if (inherited.length === 0) return [];
  const own = new Set((agent.skills ?? []).map((skill) => skill.id));
  const seen = new Set<string>();
  const chips: AgentSkillSummary[] = [];
  for (const skill of inherited) {
    if (own.has(skill.id) || seen.has(skill.id)) continue;
    seen.add(skill.id);
    chips.push(skill);
  }
  return chips;
}

/**
 * True when there is an inherited prompt to show the viewer.
 *
 * An empty `inherited_instructions` on a specialisation has two causes — the
 * base role genuinely has no prompt, or the base role is private to someone
 * else and the server serves the relationship without the text. Neither is
 * renderable, and the copy for "nothing inherited (or not visible here)"
 * covers both honestly; what must NOT happen is inventing a parent half for
 * the effective-prompt preview.
 */
export function hasInheritedPrompt(
  agent: Pick<Agent, "parent_agent_id" | "inherited_instructions">,
): boolean {
  return (
    isSpecialization(agent) &&
    (agent.inherited_instructions ?? "").length > 0
  );
}

/**
 * The stable code the archive handler attaches when a base role still has
 * specialisations (`server/internal/handler/agent.go`). The refusal carries
 * the child names as well, but the client already holds the children — it
 * needs their IDs to offer "solidify & unbind", which the body does not carry.
 */
export const AGENT_HAS_CHILDREN_CODE = "agent_has_children";

export function isAgentHasChildrenError(err: unknown): boolean {
  return errorCode(err) === AGENT_HAS_CHILDREN_CODE;
}
