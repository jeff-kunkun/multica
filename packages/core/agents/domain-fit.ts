/**
 * Domain fit (DENE-1477): which agents suit one issue or project. The client
 * twin of server `routing.DomainFit`; both read the case table in
 * `domain-fit.cases.json`.
 *
 * - Scene: the issue's own domain, else its project's domains, else generic
 *   (an empty list).
 * - match (对口): a specialisation whose domain is in the scene; in a generic
 *   scene, a base role.
 * - generic (通用): a base role in a scene that has domains — the fallback.
 * - other (其他): a specialisation for another domain; still selectable.
 *
 * Pure: no React, no storage. Mobile imports it via
 * `@multica/core/agents/domain-fit`.
 */

export type DomainFit = "match" | "generic" | "other";

const RANK: Record<DomainFit, number> = { match: 0, generic: 1, other: 2 };

export function domainScene({
  issueDomainId,
  projectDomainIds,
}: {
  issueDomainId?: string | null;
  /** Every domain on the project(s) in play; duplicates are dropped. */
  projectDomainIds?: readonly string[] | null;
}): string[] {
  if (issueDomainId) return [issueDomainId];
  return [...new Set(projectDomainIds ?? [])];
}

export function agentDomainFit(
  scene: readonly string[],
  agent: { domain_id?: string | null },
): DomainFit {
  if (!agent.domain_id) return scene.length === 0 ? "match" : "generic";
  return scene.includes(agent.domain_id) ? "match" : "other";
}

/** Stable sort by fit: 对口 → 通用 → 其他; input order holds inside a group. */
export function sortAgentsByDomainFit<T extends { domain_id?: string | null }>(
  agents: readonly T[],
  scene: readonly string[],
): T[] {
  return agents
    .map((agent, i) => ({ agent, i, rank: RANK[agentDomainFit(scene, agent)] }))
    .sort((a, b) => a.rank - b.rank || a.i - b.i)
    .map((x) => x.agent);
}
