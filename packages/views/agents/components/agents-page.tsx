"use client";

import { useMemo, useState } from "react";
import { AlertCircle, Bot, Plus } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent, AgentRuntime } from "@multica/core/types";
import {
  type AgentActivity,
  type AgentPresenceDetail,
  isAgentRuntimeBound,
  useWorkspaceActivityMap,
  useWorkspacePresenceMap,
} from "@multica/core/agents";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  readRuntimeMagpieRoute,
  runtimeListOptions,
} from "@multica/core/runtimes";
import {
  agentListOptions,
  memberListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { AppLink, useNavigation, useRowLink } from "../../navigation";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import {
  isAddressLikeName,
  splitRuntimeName,
} from "../../runtimes/components/runtime-machines";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { availabilityConfig } from "../presence";
import { MagpieRouteLine } from "./magpie-route-line";
import { useT } from "../../i18n";

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
  canManage,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  presence: AgentPresenceDetail | null;
  lastActiveDays: number | null;
  canManage: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const rowLink = useRowLink();
  const qc = useQueryClient();
  const [pending, setPending] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);

  const isArchived = !!agent.archived_at;
  const activeTasks = presence
    ? presence.runningCount + presence.queuedCount
    : 0;
  const route = isArchived ? null : readRuntimeMagpieRoute(runtime?.metadata);
  const detailHref = paths.agentDetail(agent.id);
  const hasStatus =
    !isArchived && (!isAgentRuntimeBound(agent) || presence !== null);

  const subParts = [
    isArchived ? t(($) => $.row.archived) : null,
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

  const setActive = async (active: boolean) => {
    setPending(true);
    try {
      if (active) {
        await api.restoreAgent(agent.id);
      } else {
        await api.archiveAgent(agent.id);
      }
      await qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(
        active
          ? t(($) => $.list.restored_toast, { name: agent.name })
          : t(($) => $.list.archived_toast, { name: agent.name }),
      );
    } catch (e) {
      toast.error(
        e instanceof Error
          ? e.message
          : active
            ? t(($) => $.list.restore_failed_toast)
            : t(($) => $.list.archive_failed_toast),
      );
    } finally {
      setPending(false);
    }
  };

  const onToggle = (active: boolean) => {
    // Archiving cancels the agent's tasks, so that needs a yes first.
    if (!active && activeTasks > 0) {
      setConfirmArchive(true);
      return;
    }
    void setActive(active);
  };

  return (
    <div
      className={cn(
        "flex cursor-pointer items-start gap-2.5 border-t px-3 py-3 first:border-t-0 hover:bg-accent/40 @xl:items-center @xl:gap-3.5 @xl:px-4",
        isArchived && "opacity-50",
      )}
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
        {route && (
          <MagpieRouteLine
            route={route}
            className="mt-1.5 @xl:flex-nowrap @xl:overflow-hidden @xl:whitespace-nowrap"
          />
        )}
      </div>

      {hasStatus && (
        <span className="hidden shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-caption text-muted-foreground @xl:inline-flex">
          <StatusLabel agent={agent} presence={presence} />
        </span>
      )}
      <AppLink
        href={detailHref}
        onClick={(e) => e.stopPropagation()}
        className="hidden shrink-0 rounded-md px-1.5 py-1 text-caption hover:bg-accent @xl:inline"
      >
        {t(($) => $.list.edit)}
      </AppLink>
      {/* The row navigates on click; the switch and its dialog (portaled,
          but still bubbling through React) must not. */}
      <span
        className="mt-1 flex shrink-0 @xl:mt-0"
        onClick={(e) => e.stopPropagation()}
      >
        <Switch
          checked={!isArchived}
          disabled={pending || !canManage || !!agent.system_key}
          onCheckedChange={onToggle}
          aria-label={t(($) => $.list.active_aria, { name: agent.name })}
        />
        {confirmArchive && (
          <AlertDialog
            open
            onOpenChange={(open) => {
              if (!open) setConfirmArchive(false);
            }}
          >
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {t(($) => $.list.archive_confirm_title, { name: agent.name })}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {t(($) => $.list.archive_confirm_description, {
                    count: activeTasks,
                  })}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>
                  {t(($) => $.list.archive_confirm_cancel)}
                </AlertDialogCancel>
                <AlertDialogAction
                  variant="destructive"
                  onClick={() => {
                    setConfirmArchive(false);
                    void setActive(false);
                  }}
                >
                  {t(($) => $.list.archive_confirm)}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        )}
      </span>
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
          <Skeleton className="h-5 w-9 rounded-full" />
        </div>
      ))}
    </div>
  );
}

export function AgentsPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUser = useAuthStore((s) => s.user);

  const {
    data: agents = [],
    isLoading,
    error: listError,
    refetch: refetchList,
  } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);
  const { byAgent: activityMap } = useWorkspaceActivityMap(wsId);

  const runtimesById = useMemo(
    () => new Map(runtimes.map((r) => [r.id, r])),
    [runtimes],
  );
  const isWorkspaceOwner =
    !!currentUser &&
    members.some((m) => m.user_id === currentUser.id && m.role === "owner");

  // Active agents first, archived ones at the bottom; otherwise list order.
  const sorted = useMemo(
    () => [
      ...agents.filter((a) => !a.archived_at),
      ...agents.filter((a) => a.archived_at),
    ],
    [agents],
  );
  const activeCount =
    agents.length - agents.filter((a) => a.archived_at).length;
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

  if (!isLoading && agents.length === 0) {
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
      <PageHeaderBar count={activeCount} onCreate={onCreate} />
      <div className="min-h-0 flex-1 overflow-y-auto @container">
        <div className="mx-auto w-full max-w-4xl p-3 @xl:p-6">
          {isLoading ? (
            <LoadingSkeleton />
          ) : (
            <div className="overflow-hidden rounded-xl border bg-card">
              {sorted.map((agent) => {
                const isOwner =
                  !!currentUser && agent.owner_id === currentUser.id;
                return (
                  <AgentRow
                    key={agent.id}
                    agent={agent}
                    runtime={runtimesById.get(agent.runtime_id) ?? null}
                    presence={presenceMap.get(agent.id) ?? null}
                    lastActiveDays={lastActiveDaysAgo(
                      activityMap.get(agent.id) ?? null,
                    )}
                    canManage={isWorkspaceOwner || isOwner}
                  />
                );
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
