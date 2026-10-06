"use client";

import { useMemo } from "react";
import type { Agent, Project } from "@multica/core/types";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { PickerEmpty, PickerSection } from "../../issues/components/pickers/property-picker";
import { useT } from "../../i18n";

export type AgentGroupKind = "mine" | "others" | "fit" | "rest";

export interface AgentGroup {
  kind: AgentGroupKind;
  agents: Agent[];
}

/**
 * Groups the agent list for the "new chat" pickers. One function so the ⊕
 * button and the in-chat dropdown never drift apart.
 *
 * - No project in play: the long-standing "my agents / others" split.
 * - Projects in play: the first group is the agents that fit (DENE-1451
 *   domains) and the rest follow. The project split replaces "mine / others",
 *   two competing splits in one 256px popover read as noise.
 *   - The projects carry domains (union across projects): fit = agents whose
 *     `domain_id` is in that set.
 *   - Generic projects (no domain): fit = base roles, i.e. agents with no
 *     `domain_id`.
 *
 * Order inside a group follows the input order. Search runs over every agent,
 * hits are then grouped the same way; empty groups are dropped.
 */
export function groupAgentsForPicker({
  agents,
  userId,
  projects,
  query = "",
}: {
  agents: Agent[];
  userId: string | undefined;
  projects: readonly Project[];
  query?: string;
}): AgentGroup[] {
  const q = query.trim().toLowerCase();
  const hits = q
    ? agents.filter((a) => a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q))
    : agents;

  const groups: AgentGroup[] = [];
  if (projects.length === 0) {
    groups.push(
      { kind: "mine", agents: hits.filter((a) => a.owner_id === userId) },
      { kind: "others", agents: hits.filter((a) => a.owner_id !== userId) },
    );
  } else {
    const domainIds = new Set(projects.flatMap((p) => p.domain_ids ?? []));
    const fits = (a: Agent) =>
      domainIds.size > 0 ? !!a.domain_id && domainIds.has(a.domain_id) : !a.domain_id;
    groups.push(
      { kind: "fit", agents: hits.filter(fits) },
      { kind: "rest", agents: hits.filter((a) => !fits(a)) },
    );
  }
  return groups.filter((g) => g.agents.length > 0);
}

/** Sections for the picker body: grouped, headed by one word, empty-aware. */
export function AgentPickerGroups({
  agents,
  userId,
  projects,
  query,
  renderAgent,
}: {
  agents: Agent[];
  userId: string | undefined;
  projects: readonly Project[];
  query: string;
  renderAgent: (agent: Agent) => React.ReactNode;
}) {
  const { t } = useT("chat");
  const groups = useMemo(
    () => groupAgentsForPicker({ agents, userId, projects, query }),
    [agents, userId, projects, query],
  );
  if (groups.length === 0) return <PickerEmpty />;

  const fitLabel =
    projects.length === 1
      ? t(($) => $.window.fit_project, { name: projects[0]!.title })
      : t(($) => $.window.fit_projects);
  const labels: Record<AgentGroupKind, string> = {
    mine: t(($) => $.window.my_agents),
    others: t(($) => $.window.others),
    fit: fitLabel,
    rest: t(($) => $.window.others),
  };
  return (
    <>
      {groups.map((g) => (
        <PickerSection key={g.kind} label={labels[g.kind]}>
          {g.agents.map(renderAgent)}
        </PickerSection>
      ))}
    </>
  );
}
