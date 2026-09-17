"use client";

import { Lock } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { isSpecialization, parentAgentLabel } from "../specialization";

/**
 * The one read-only banner every inheritance-aware surface renders for a
 * specialisation: the configuration below it belongs to the base role, so the
 * only affordance left is the link to where it is edited.
 *
 * The href comes from the child's own `parent_agent_id`, never from the loaded
 * row: when the base role is private to another member the row is missing from
 * the list and this notice must still point at it. `parentAgent` only upgrades
 * the name and the values the callers show.
 */
export function InheritedConfigNotice({
  agent,
  parentAgent,
  message,
}: {
  agent: Agent;
  /** The base-role row when the caller's list holds it. */
  parentAgent?: Agent | null;
  /** Surface-specific body copy; defaults to the generic inherited line. */
  message?: string;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  if (!isSpecialization(agent)) return null;

  const name = parentAgentLabel(agent, parentAgent);
  const notice =
    message ??
    (name
      ? t(($) => $.specialization.inherited_config_notice, { name })
      : t(($) => $.specialization.inherited_config_notice_unnamed));

  return (
    <div
      role="status"
      data-testid="agent-inherited-config"
      className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-caption text-muted-foreground"
    >
      <Lock aria-hidden="true" className="size-3.5 shrink-0" />
      <p className="min-w-0 flex-1 leading-snug">{notice}</p>
      <AppLink
        href={paths.agentDetail(agent.parent_agent_id ?? "")}
        newTabTitle={name || undefined}
        className="shrink-0 font-medium text-foreground underline-offset-4 hover:underline"
      >
        {t(($) => $.specialization.inherited_config_open)}
      </AppLink>
    </div>
  );
}
