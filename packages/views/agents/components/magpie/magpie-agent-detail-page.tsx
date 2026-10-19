"use client";

import {
  AlertCircle,
  ArrowLeft,
  Clock3,
  Lock,
  MessageSquare,
  Plus,
  Server,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import {
  type AgentPresenceDetail,
  isAgentRuntimeBound,
  useWorkspacePresenceMap,
} from "@multica/core/agents";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentDetailOptions,
  agentListOptions,
  memberListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";
import { useAgentPermissions } from "@multica/core/permissions";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { AppLink, useNavigation } from "../../../navigation";
import { PAGE_GUTTER, PAGE_RAIL, PageHeader } from "../../../layout/page-header";
import { ActorAvatar } from "../../../common/actor-avatar";
import { runtimeCliLabel } from "../../../runtimes/components/runtime-machines";
import { AgentPresenceIndicator } from "../agent-presence-indicator";
import { VisibilityBadge } from "../visibility-badge";
import { MagpieAgentOverviewPane } from "./magpie-agent-overview-pane";
import { ExpandableDescription } from "../../../common/expandable-description";
import { useT, useTimeAgo } from "../../../i18n";

interface AgentDetailPageProps {
  agentId: string;
}

export function MagpieAgentDetailPage({ agentId }: AgentDetailPageProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();

  const {
    data: agents = [],
    isLoading: agentsLoading,
    error: agentsError,
    refetch: refetchAgents,
  } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));

  // Single workspace-level presence pass; this page just reads its slot.
  // The hook owns the 30s tick so the failed-window auto-clears here too.
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);

  const listAgent = agents.find((a) => a.id === agentId) ?? null;

  // The list remains the zero-request common path. When it has settled
  // without the requested agent, use the canonical detail query to resolve
  // direct links and distinguish 403/404 from transient failures. Creation
  // hydrates this same key, so a newly-created agent renders immediately.
  const detailQuery = useQuery({
    ...agentDetailOptions(wsId, agentId),
    enabled: !agentsLoading && !listAgent && !!agentId,
  });
  const detailError = detailQuery.error;
  const isForbidden =
    detailError instanceof ApiError && detailError.status === 403;
  const isNotFound =
    detailError instanceof ApiError && detailError.status === 404;
  // TanStack intentionally keeps successful data when a refetch fails. Do not
  // let that stale snapshot mask a later 403/404 after access is revoked or the
  // agent is deleted, but preserve it through transient network failures. A
  // still-visible list response remains authoritative in either case.
  const agent =
    listAgent ?? (isForbidden || isNotFound ? null : detailQuery.data) ?? null;
  const presence: AgentPresenceDetail | null =
    agent ? presenceMap.get(agent.id) ?? null : null;

  // Permission hook MUST be called unconditionally — its `agent | null`
  // signature handles the not-found / loading case internally so the early
  // returns below don't violate the rules of hooks.
  const {
    canEdit,
    canAssign,
    isLoading: permissionsLoading,
  } = useAgentPermissions(
    agent,
    wsId,
  );

  // --- Loading ---
  if (!agent && (agentsLoading || detailQuery.isPending)) {
    return <DetailLoadingSkeleton />;
  }

  // --- No permission (private agent the caller is not in allowed_principals for) ---
  if (!agent && isForbidden) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <BackHeader paths={paths.agents()} title={t(($) => $.detail.back_to_agents)} />
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-16 text-center">
          <Lock className="h-8 w-8 text-muted-foreground" />
          <div>
            <p className="text-body font-medium">{t(($) => $.detail.no_access_title)}</p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.detail.no_access_hint)}
            </p>
          </div>
          <Button
            size="sm"
            render={<AppLink href={paths.agents()} />}
            nativeButton={false}
          >
            {t(($) => $.detail.back_to_agents_full)}
          </Button>
        </div>
      </div>
    );
  }

  // --- Not found / error ---
  if (!agent) {
    const loadError = detailError ?? agentsError;
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <BackHeader paths={paths.agents()} title={t(($) => $.detail.back_to_agents)} />
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-16 text-center">
          <AlertCircle className="h-8 w-8 text-destructive" />
          <div>
            <p className="text-body font-medium">
              {isNotFound
                ? t(($) => $.detail.not_found_title)
                : t(($) => $.detail.load_failed_title)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {isNotFound
                ? t(($) => $.detail.not_found_default)
                : loadError instanceof Error
                  ? loadError.message
                  : t(($) => $.detail.load_failed_default)}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                void Promise.all([refetchAgents(), detailQuery.refetch()]);
              }}
            >
              {t(($) => $.detail.try_again)}
            </Button>
            <Button
              size="sm"
              render={<AppLink href={paths.agents()} />}
              nativeButton={false}
            >
              {t(($) => $.detail.back_to_agents_full)}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const isArchived = !!agent.archived_at;
  const runtimeBound = isAgentRuntimeBound(agent);
  const runtime = runtimeBound
    ? runtimes.find((r) => r.id === agent.runtime_id) ?? null
    : null;
  const owner = agent.owner_id
    ? members.find((m) => m.user_id === agent.owner_id) ?? null
    : null;

  // Chat shares the invocation gate with assignment (MUL-3963): starting a
  // chat triggers agent runs. The button stays visible either way — a denied
  // click explains itself instead of the affordance silently missing. While
  // membership is still resolving the decision is undetermined, so the button
  // is disabled rather than toasting a false "no access" at a real member.
  //
  // The control is a real link, so a failed gate has to cancel the navigation
  // AppLink would otherwise perform — preventDefault is that cancel.
  const handleDm = (e: React.MouseEvent<HTMLAnchorElement>) => {
    if (permissionsLoading) {
      e.preventDefault();
      return;
    }
    if (!canAssign.allowed) {
      e.preventDefault();
      toast.error(t(($) => $.detail.dm_no_permission_toast));
      return;
    }
    if (!runtimeBound) {
      e.preventDefault();
      toast.error(t(($) => $.detail.runtime_required_toast));
    }
  };
  const handleAssign = () => {
    if (!runtimeBound) {
      toast.error(t(($) => $.detail.runtime_required_toast));
      return;
    }
    useModalStore
      .getState()
      .open("create-issue", { assignee_type: "agent", assignee_id: agent.id });
  };

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <DetailHeader
        agent={agent}
        runtime={runtime}
        presence={presence}
        backHref={paths.agents()}
        canAssign={canAssign.allowed}
        canDelete={canEdit.allowed && !agent.system_key}
        dmPending={permissionsLoading}
        dmHref={`${paths.chat()}?agent=${agent.id}`}
        onDm={handleDm}
        onAssign={handleAssign}
      />

      {isArchived && (
        <div className="shrink-0 border-b bg-muted/50 py-2 text-caption text-muted-foreground">
          <div className={cn(PAGE_RAIL, PAGE_GUTTER, "flex items-center gap-2")}>
            <AlertCircle className="h-3.5 w-3.5 shrink-0" />
            <span className="flex-1">
              {t(($) => $.detail.archived_banner)}
            </span>
          </div>
        </div>
      )}

      {!isArchived && !runtimeBound && (
        <div className="shrink-0 border-b border-amber-500/30 bg-amber-500/10 py-2 text-caption text-amber-900 dark:text-amber-100">
          <div className={cn(PAGE_RAIL, PAGE_GUTTER, "flex items-center gap-2")}>
            <Server className="h-3.5 w-3.5 shrink-0" />
            <span className="flex-1">
              {t(($) => $.detail.runtime_required_banner)}
            </span>
          </div>
        </div>
      )}

      <div className="flex flex-1 min-h-0 flex-col overflow-hidden">
        <MagpieAgentOverviewPane agent={agent} runtime={runtime} owner={owner} />
      </div>
    </div>
  );
}

function DetailHeader({
  agent,
  runtime,
  presence,
  backHref,
  canAssign,
  canDelete,
  dmPending,
  dmHref,
  onDm,
  onAssign,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  presence: AgentPresenceDetail | null;
  backHref: string;
  canAssign: boolean;
  canDelete: boolean;
  dmPending: boolean;
  dmHref: string;
  /** Runs before the link navigates; calls preventDefault when a gate denies
   *  the chat, which is what stops AppLink from pushing. */
  onDm: (e: React.MouseEvent<HTMLAnchorElement>) => void;
  onAssign: () => void;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const isArchived = !!agent.archived_at;

  return (
    <header
      className="shrink-0 border-b bg-background pb-5 pt-3"
    >
      <div className={cn(PAGE_RAIL, PAGE_GUTTER)}>
        <div className="flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
          <AppLink
            href={backHref}
            className="rounded-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {t(($) => $.page.title)}
          </AppLink>
          <span aria-hidden="true">/</span>
          <span className="truncate text-foreground">{agent.name}</span>
        </div>

        <div className="mt-4 flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="flex min-w-0 items-start gap-4">
            <ActorAvatar
              actorType="agent"
              actorId={agent.id}
              size="2xl"
              profileLink={false}
              className="ring-1 ring-border"
            />
            <div className="min-w-0 pt-0.5">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <h1 className="min-w-0 text-balance text-title-lg font-semibold tracking-tight sm:text-display-sm">
                  {agent.name}
                </h1>
                <AgentPresenceIndicator detail={presence} />
              </div>
              <ExpandableDescription>
                {agent.description ||
                  t(($) => $.inspector.no_description_placeholder)}
              </ExpandableDescription>
              <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-caption text-muted-foreground">
                <span className="inline-flex min-w-0 items-center gap-1.5">
                  <Server className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  <span className="truncate">
                    {runtime
                      ? runtimeCliLabel(runtime)
                      : t(($) => $.pickers.runtime_none)}
                  </span>
                </span>
                <VisibilityBadge value={agent.visibility} />
                <span className="inline-flex items-center gap-1.5">
                  <Clock3 className="h-3.5 w-3.5" aria-hidden="true" />
                  {t(($) => $.detail.updated, { when: timeAgo(agent.updated_at) })}
                </span>
              </div>
            </div>
          </div>

          <div className="flex shrink-0 items-center gap-2 self-end lg:self-start">
            {!isArchived && (
              <Button
                variant="outline"
                size="sm"
                disabled={dmPending}
                // An anchor never matches `:disabled`, so the base variant's
                // `disabled:` rules never fire here — Base UI's data-disabled
                // is what carries the dimmed, inert look.
                className="data-disabled:pointer-events-none data-disabled:opacity-50"
                render={<AppLink href={dmHref} onClick={onDm} />}
                nativeButton={false}
              >
                <MessageSquare className="h-4 w-4" aria-hidden="true" />
                {t(($) => $.detail.dm)}
              </Button>
            )}
            {!isArchived && canAssign && (
              <Button type="button" size="sm" onClick={onAssign}>
                <Plus className="h-4 w-4" aria-hidden="true" />
                {t(($) => $.detail.assign_work)}
              </Button>
            )}
            {canDelete && (
              <DeleteAgentButton
                agent={agent}
                activeRuns={
                  (presence?.runningCount ?? 0) + (presence?.queuedCount ?? 0)
                }
                backHref={backHref}
              />
            )}
          </div>
        </div>
      </div>
    </header>
  );
}

/**
 * Deletes the agent for good. The server backs its full configuration up
 * first and refuses to delete when the backup fails, so the reason it gives
 * is shown as-is.
 */
function DeleteAgentButton({
  agent,
  activeRuns,
  backHref,
}: {
  agent: Agent;
  activeRuns: number;
  backHref: string;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => api.deleteAgent(agent.id),
    onSuccess: () => {
      // Leave first: refetching the list while this page is mounted would
      // flash its not-found state for the agent that is now gone.
      setOpen(false);
      toast.success(t(($) => $.detail.agent_deleted_toast));
      navigation.push(backHref);
      void queryClient.invalidateQueries({
        queryKey: workspaceKeys.agents(wsId),
      });
    },
    onError: (err) => {
      toast.error(t(($) => $.detail.delete_failed_toast), {
        description: err instanceof Error ? err.message : undefined,
      });
    },
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-4 w-4" aria-hidden="true" />
        {t(($) => $.detail.delete)}
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t(($) => $.detail.delete_dialog_title, { name: agent.name })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {activeRuns > 0
              ? t(($) => $.detail.delete_dialog_active_runs, {
                  count: activeRuns,
                })
              : t(($) => $.detail.delete_dialog_description)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={remove.isPending}>
            {t(($) => $.detail.delete_dialog_cancel)}
          </AlertDialogCancel>
          <Button
            type="button"
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {t(($) => $.detail.delete_dialog_confirm)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function BackHeader({ paths, title }: { paths: string; title: string }) {
  return (
    <PageHeader>
      <AppLink
        href={paths}
        className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-caption text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        {title}
      </AppLink>
    </PageHeader>
  );
}

function DetailLoadingSkeleton() {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <div className="shrink-0 border-b pb-5 pt-3">
        <div className={cn(PAGE_RAIL, PAGE_GUTTER)}>
          <Skeleton className="h-4 w-48" />
          <div className="mt-4 flex items-start gap-4">
            <Skeleton className="h-14 w-14 rounded-full" />
            <div className="flex-1 space-y-3">
              <Skeleton className="h-7 w-64" />
              <Skeleton className="h-4 w-full max-w-xl" />
              <Skeleton className="h-4 w-full max-w-lg" />
            </div>
          </div>
        </div>
      </div>
      <div className={cn(PAGE_RAIL, PAGE_GUTTER, "flex flex-1 flex-col py-6")}>
        <Skeleton className="h-9 w-96" />
        <div className="mt-6 grid flex-1 gap-6 xl:grid-cols-[minmax(0,1fr)_320px]">
          <div className="space-y-5">
            <Skeleton className="h-48 w-full" />
            <Skeleton className="h-64 w-full" />
          </div>
          <Skeleton className="h-96 w-full" />
        </div>
      </div>
    </div>
  );
}
