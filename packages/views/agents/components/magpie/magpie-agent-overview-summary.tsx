"use client";

import { Server } from "lucide-react";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import { canPinMagpieGroup, readRuntimeMagpieRoute } from "@multica/core/runtimes";
import { runtimeCliLabel } from "../../../runtimes/components/runtime-machines";
import { ActorAvatar } from "../../../common/actor-avatar";
import { useT } from "../../../i18n";
import { VisibilityBadge } from "../visibility-badge";
import { AgentPerformanceSummary } from "../tabs/activity-tab";
import { MagpieGroupPicker } from "./magpie-group-picker";

interface AgentOverviewSummaryProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  owner: MemberWithUser | null;
}

/**
 * Context for the workbench Overview. The magpie group is its one control:
 * with no settings page, this is where an agent is pinned to a group.
 */
export function MagpieAgentOverviewSummary({
  agent,
  runtime,
  owner,
}: AgentOverviewSummaryProps) {
  const { t } = useT("agents");
  const runtimeOnline = runtime?.status === "online";
  const magpieRoute = readRuntimeMagpieRoute(runtime?.metadata);

  return (
    <aside className="self-start rounded-xl border border-surface-border bg-surface p-5 shadow-[var(--surface-shadow)] xl:sticky xl:top-6">
      <section>
        <h2 className="text-body font-medium">
          {t(($) => $.overview.agent_context)}
        </h2>
        <dl className="mt-4 space-y-3 text-caption">
          {owner && (
            <SummaryRow label={t(($) => $.inspector.prop_owner)}>
              <span className="flex min-w-0 items-center gap-1.5">
                <ActorAvatar
                  actorType="member"
                  actorId={owner.user_id}
                  size="xs"
                />
                <span className="truncate text-foreground">{owner.name}</span>
              </span>
            </SummaryRow>
          )}
          <SummaryRow label={t(($) => $.overview.access)}>
            <VisibilityBadge value={agent.visibility} />
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_cli)}>
            <span className="flex min-w-0 items-center gap-1.5 text-foreground">
              <span
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                  runtimeOnline ? "bg-success" : "bg-muted-foreground/40"
                }`}
                aria-hidden="true"
              />
              <Server className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="truncate">
                {runtime
                  ? runtimeCliLabel(runtime)
                  : t(($) => $.pickers.runtime_none)}
              </span>
            </span>
          </SummaryRow>
          {runtime &&
            magpieRoute &&
            magpieRoute.groups.length > 0 &&
            canPinMagpieGroup(runtime.provider) && (
              <SummaryRow label={t(($) => $.magpie.group_label)} alignTop>
                <MagpieGroupPicker agent={agent} route={magpieRoute} />
              </SummaryRow>
            )}
          <SummaryRow label={t(($) => $.inspector.prop_concurrency)}>
            <span className="font-mono tabular-nums text-foreground">
              {agent.max_concurrent_tasks}
            </span>
          </SummaryRow>
        </dl>
      </section>

      <AgentPerformanceSummary agent={agent} />
    </aside>
  );
}

function SummaryRow({
  label,
  alignTop,
  children,
}: {
  label: string;
  alignTop?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div
      className={`grid grid-cols-[88px_minmax(0,1fr)] gap-3 ${alignTop ? "items-start" : "items-center"}`}
    >
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
