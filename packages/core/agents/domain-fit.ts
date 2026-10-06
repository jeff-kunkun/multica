/**
 * Domain fit (DENE-1477) — "does this agent suit this work", the client half.
 * The server rule lives in server/internal/routing/domainfit.go; both read the
 * case table domain-fit.cases.json, so a picker and the router never disagree
 * about who fits.
 *
 * - The scene is the issue's own domain, else its projects' domains (union),
 *   else generic (no domains).
 * - match (对口): a specialisation whose domain is in the scene; in a generic
 *   scene, a base role.
 * - generic (通用): a base role in a scene that has domains — the fallback.
 * - other (其他): a specialisation for another domain. Still pickable, last.
 *
 * Pure: no React, no storage, so the mobile app reads the same file.
 */

export type DomainFit = "match" | "generic" | "other";

/** Group order: match first, other last. */
export const DOMAIN_FIT_ORDER: readonly DomainFit[] = ["match", "generic", "other"];

/** Minimal project shape the scene needs (Project satisfies it). */
export interface DomainSceneProject {
  id: string;
  title: string;
  domain_ids?: string[] | null;
}

/**
 * Where a pick is made. `null` at the call site means "no project in play":
 * pickers keep their own order and do not group.
 */
export interface AgentScene {
  /** Scene domains; empty = generic. */
  domains: string[];
  /** The projects in play, for the group heading. */
  projects: DomainSceneProject[];
}

/** The scene rule: the issue's own domain, else the projects' domains. */
export function resolveSceneDomains(
  issueDomainId: string | null | undefined,
  projectDomainIds: readonly (string | null | undefined)[],
): string[] {
  const own = issueDomainId?.trim();
  if (own) return [own];
  const out: string[] = [];
  for (const raw of projectDomainIds) {
    const d = raw?.trim();
    if (d && !out.includes(d)) out.push(d);
  }
  return out;
}

/** The scene for some projects (and optionally an issue); null without projects. */
export function agentSceneOf(
  projects: readonly DomainSceneProject[],
  issueDomainId?: string | null,
): AgentScene | null {
  if (projects.length === 0) return null;
  return {
    domains: resolveSceneDomains(
      issueDomainId,
      projects.flatMap((p) => p.domain_ids ?? []),
    ),
    projects: [...projects],
  };
}

/** The rule. `agentDomainId` is the specialisation's domain, empty for a base role. */
export function domainFit(
  sceneDomains: readonly string[],
  agentDomainId: string | null | undefined,
): DomainFit {
  if (!agentDomainId) return sceneDomains.length === 0 ? "match" : "generic";
  return sceneDomains.includes(agentDomainId) ? "match" : "other";
}

export interface AgentFitGroup<T> {
  fit: DomainFit;
  items: T[];
}

/** Groups in rank order, input order kept inside each, empty groups dropped. */
export function groupAgentsByFit<T extends { domain_id?: string | null }>(
  agents: readonly T[],
  scene: AgentScene,
): AgentFitGroup<T>[] {
  return DOMAIN_FIT_ORDER.map((fit) => ({
    fit,
    items: agents.filter((a) => domainFit(scene.domains, a.domain_id) === fit),
  })).filter((g) => g.items.length > 0);
}

/** Stable sort by fit rank; the input order breaks ties. */
export function sortAgentsByFit<T extends { domain_id?: string | null }>(
  agents: readonly T[],
  scene: AgentScene,
): T[] {
  return groupAgentsByFit(agents, scene).flatMap((g) => g.items);
}
