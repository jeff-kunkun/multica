"use client";

import { useMemo } from "react";
import { AlertCircle, Bot, Plus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import {
  type AgentActivity,
  type AgentPresenceDetail,
  agentTaskSnapshotOptions,
  isAgentRuntimeBound,
  useWorkspaceActivityMap,
  useWorkspacePresenceMap,
} from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions } from "@multica/core/issues";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentMagpieRoutes,
  readRuntimeMagpieGroupRoute,
  runtimeListOptions,
} from "@multica/core/runtimes";
import { agentListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { AppLink, useNavigation, useRowLink } from "../../../navigation";
import { ProviderLogo } from "../../../runtimes/components/provider-logo";
import {
  isAddressLikeName,
  splitRuntimeName,
} from "../../../runtimes/components/runtime-machines";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../../layout/collection-page";
import { availabilityConfig } from "../../presence";
import { MagpieRouteLine } from "./magpie-route-line";
import { MagpieSwitchRow } from "./magpie-switch-row";
import { useT } from "../../../i18n";

// Most recent activity bucket with runs, as "days ago" (0 = today), from the
// same 30-day buckets the detail page charts.
function lastActiveDaysAgo(activity: AgentActivity | null): number | null {
  if (!activity) return null;
  for (let i = activity.buckets.length - 1; i >= 0; i--) {
    const bucket = activity.buckets[i];
    if (bucket && bucket.total > 0) return activity.buckets.length - 1 - i;
  }
  return null;
}

// The computer the runtime runs on; an address-shaped hostname says nothing.
function runtimeMachine(runtime: AgentRuntime | null): string | null {
  if (!runtime) return null;
  const { hostname } = splitRuntimeName(runtime.name);
  return hostname && !isAddressLikeName(hostname) ? hostname : null;
}

function PageHeaderBar({
  count,
  onCreate,
}: {
  count: number;
  onCreate: () => void;
}) {
  const { t } = useT("agents");
  return (
    <>
      <CollectionPageHeader
        icon={Bot}
        title={t(($) => $.page.title)}
        count={count}
        actions={
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.page.new_agent)}
            onClick={onCreate}
          />
        }
      />
      <MagpieSwitchRow />
    </>
  );
}

/** The issue the agent is running right now, linked; nothing while idle. */
function CurrentWork({ issueId }: { issueId: string }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { data: issue } = useQuery(issueDetailOptions(wsId, issueId));
  if (!issue) return null;
  return (
    <div className="mt-1 flex min-w-0 items-center gap-1.5 text-caption">
      <span className="shrink-0 text-muted-foreground">
        {t(($) => $.list.current_work)}
      </span>
      <AppLink
        href={paths.issueDetail(issueId)}
        onClick={(e) => e.stopPropagation()}
        className="min-w-0 truncate hover:underline"
        title={`${issue.identifier} ${issue.title}`}
      >
        <span className="mr-1 font-mono text-micro text-muted-foreground">
          {issue.identifier}
        </span>
        {issue.title}
      </AppLink>
    </div>
  );
}

/** Status in a word or two: the chip on wide screens, a prefix on narrow. */
function StatusLabel({
  agent,
  presence,
}: {
  agent: Agent;
  presence: AgentPresenceDetail | null;
}) {
  const { t } = useT("agents");
  if (!isAgentRuntimeBound(agent)) {
    return (
      <>
        <span className="size-1.5 shrink-0 rounded-full bg-warning" />
        {t(($) => $.list.needs_runtime)}
      </>
    );
  }
  if (!presence) return null;
  const active = presence.runningCount + presence.queuedCount;
  if (active > 0) {
    return (
      <>
        <span className="size-1.5 shrink-0 rounded-full bg-brand" />
        {t(($) => $.list.working, { count: active })}
      </>
    );
  }
  return (
    <>
      <span
        className={cn(
          "size-1.5 shrink-0 rounded-full",
          availabilityConfig[presence.availability].dotClass,
        )}
      />
      {t(($) => $.availability[presence.availability])}
    </>
  );
}

function AgentRow({
  agent,
  runtime,
  presence,
  lastActiveDays,
  currentIssueId,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  presence: AgentPresenceDetail | null;
  lastActiveDays: number | null;
  currentIssueId: string | null;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const rowLink = useRowLink();

  // Native agents (no magpie group route on their runtime) show no route.
  const routes = agentMagpieRoutes(
    readRuntimeMagpieGroupRoute(runtime?.metadata),
    runtime?.provider,
    agent,
  );
  const detailHref = paths.agentDetail(agent.id);
  const hasStatus = !isAgentRuntimeBound(agent) || presence !== null;

  const subParts = [
    agent.description || null,
    runtimeMachine(runtime),
    lastActiveDays === null
      ? t(($) => $.last_active.none)
      : t(($) => $.list.last_active, {
          when:
            lastActiveDays === 0
              ? t(($) => $.last_active.today)
              : t(($) => $.last_active.days_ago, { count: lastActiveDays }),
        }),
  ].filter(Boolean);

  return (
    <div
      className="flex cursor-pointer items-start gap-2.5 border-t px-3 py-3 first:border-t-0 hover:bg-accent/40 @xl:items-center @xl:gap-3.5 @xl:px-4"
      data-agent-row=""
      {...rowLink(detailHref, agent.name)}
    >
      <div className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted @xl:mt-0">
        {runtime ? (
          <ProviderLogo provider={runtime.provider} className="size-5" />
        ) : (
          <Bot aria-hidden="true" className="size-4 text-muted-foreground" />
        )}
      </div>

      <div className="min-w-0 flex-1">
        {/* The row is mouse-only; the name is its keyboard way in. */}
        <AppLink
          href={detailHref}
          onClick={(e) => e.stopPropagation()}
          className="block truncate text-body font-semibold"
        >
          {agent.name}
        </AppLink>
        <div className="mt-px flex min-w-0 items-center gap-1.5 truncate text-caption text-muted-foreground">
          {hasStatus && (
            <span className="inline-flex shrink-0 items-center gap-1.5 @xl:hidden">
              <StatusLabel agent={agent} presence={presence} />
              <span aria-hidden="true">·</span>
            </span>
          )}
          <span className="truncate">{subParts.join(" · ")}</span>
        </div>
        {currentIssueId && (
          <CurrentWork issueId={currentIssueId} />
        )}
        {routes.map((route, i) => (
          <MagpieRouteLine
            key={route.group || i}
            route={route}
            className={`${i === 0 ? "mt-1.5" : "mt-1"} @xl:flex-nowrap @xl:overflow-hidden @xl:whitespace-nowrap`}
          />
        ))}
      </div>

      {hasStatus && (
        <span className="hidden shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-caption text-muted-foreground @xl:inline-flex">
          <StatusLabel agent={agent} presence={presence} />
        </span>
      )}
    </div>
  );
}

function LoadingSkeleton() {
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      {Array.from({ length: 4 }).map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-3.5 border-t px-4 py-3.5 first:border-t-0"
        >
          <Skeleton className="size-8 rounded-lg" />
          <div className="min-w-0 flex-1 space-y-1.5">
            <Skeleton className="h-3.5 w-32 max-w-full" />
            <Skeleton className="h-3 w-56 max-w-full" />
            <Skeleton className="h-3 w-44 max-w-full" />
          </div>
        </div>
      ))}
    </div>
  );
}

export function MagpieAgentsPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();

  const {
    data: agents = [],
    isLoading,
    error: listError,
    refetch: refetchList,
  } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);
  const { byAgent: activityMap } = useWorkspaceActivityMap(wsId);

  const runtimesById = useMemo(
    () => new Map(runtimes.map((r) => [r.id, r])),
    [runtimes],
  );
  // The issue each agent is running now; chat runs have no issue to show.
  const currentIssueByAgent = useMemo(() => {
    const map = new Map<string, string>();
    for (const task of snapshot) {
      if (task.status === "running" && task.issue_id && !map.has(task.agent_id)) {
        map.set(task.agent_id, task.issue_id);
      }
    }
    return map;
  }, [snapshot]);

  // Archived agents predate permanent delete; the office does not show them.
  const visible = useMemo(
    () => agents.filter((a) => !a.archived_at),
    [agents],
  );
  const onCreate = () => navigation.push(paths.newAgent());

  if (listError) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <PageHeaderBar count={0} onCreate={onCreate} />
        <CollectionPageState
          role="alert"
          tone="destructive"
          icon={AlertCircle}
          title={t(($) => $.page.list_load_failed)}
          description={
            listError instanceof Error
              ? listError.message
              : t(($) => $.page.list_load_failed_default)
          }
          actions={
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => refetchList()}
            >
              {t(($) => $.page.try_again)}
            </Button>
          }
        />
      </div>
    );
  }

  if (!isLoading && visible.length === 0) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <PageHeaderBar count={0} onCreate={onCreate} />
        <div className="flex flex-1 items-center justify-center">
          <CollectionPageState
            icon={Bot}
            title={t(($) => $.empty.title)}
            description={t(($) => $.empty.description)}
            actions={
              <Button type="button" onClick={onCreate} size="sm">
                <Plus aria-hidden="true" className="size-3" />
                {t(($) => $.page.new_agent)}
              </Button>
            }
          />
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeaderBar count={visible.length} onCreate={onCreate} />
      <div className="min-h-0 flex-1 overflow-y-auto @container">
        <div className="mx-auto w-full max-w-4xl p-3 @xl:p-6">
          {isLoading ? (
            <LoadingSkeleton />
          ) : (
            <div className="overflow-hidden rounded-xl border bg-card">
              {visible.map((agent) => (
                <AgentRow
                  key={agent.id}
                  agent={agent}
                  runtime={runtimesById.get(agent.runtime_id) ?? null}
                  presence={presenceMap.get(agent.id) ?? null}
                  lastActiveDays={lastActiveDaysAgo(
                    activityMap.get(agent.id) ?? null,
                  )}
                  currentIssueId={currentIssueByAgent.get(agent.id) ?? null}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
