"use client";

import { useMemo } from "react";
import type { Agent } from "@multica/core/types";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { PickerEmpty, PickerSection } from "../../issues/components/pickers/property-picker";
import { useT } from "../../i18n";

export type AgentGroupKind = "mine" | "others";

export interface AgentGroup {
  kind: AgentGroupKind;
  agents: Agent[];
}

/**
 * Groups the agent list for the "new chat" pickers into "my agents / others".
 * One function so the ⊕ button and the in-chat dropdown never drift apart.
 *
 * Order inside a group follows the input order. Search runs over every agent,
 * hits are then grouped the same way; empty groups are dropped.
 */
export function groupAgentsForPicker({
  agents,
  userId,
  query = "",
}: {
  agents: Agent[];
  userId: string | undefined;
  query?: string;
}): AgentGroup[] {
  const q = query.trim().toLowerCase();
  const hits = q
    ? agents.filter((a) => a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q))
    : agents;
  return [
    { kind: "mine" as const, agents: hits.filter((a) => a.owner_id === userId) },
    { kind: "others" as const, agents: hits.filter((a) => a.owner_id !== userId) },
  ].filter((g) => g.agents.length > 0);
}

/** Sections for the picker body: grouped, headed by one word, empty-aware. */
export function AgentPickerGroups({
  agents,
  userId,
  query,
  renderAgent,
}: {
  agents: Agent[];
  userId: string | undefined;
  query: string;
  renderAgent: (agent: Agent) => React.ReactNode;
}) {
  const { t } = useT("chat");
  const groups = useMemo(
    () => groupAgentsForPicker({ agents, userId, query }),
    [agents, userId, query],
  );
  if (groups.length === 0) return <PickerEmpty />;

  return (
    <>
      {groups.map((g) => (
        <PickerSection
          key={g.kind}
          label={g.kind === "mine" ? t(($) => $.window.my_agents) : t(($) => $.window.others)}
        >
          {g.agents.map(renderAgent)}
        </PickerSection>
      ))}
    </>
  );
}
