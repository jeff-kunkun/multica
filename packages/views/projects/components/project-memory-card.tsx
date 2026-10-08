"use client";

import { AlertCircle, CheckCircle2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { projectMemoryOptions } from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { KnowledgeAuditChange } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";

function formatObservedAt(value: string | null | undefined, fallback: string) {
	if (!value) return fallback;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function ProjectMemoryCard({ projectId }: { projectId: string }) {
	const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const timeAgo = useTimeAgo();
  const { data, isLoading } = useQuery(projectMemoryOptions(workspaceId, projectId));
  const actionLabel = (action: NonNullable<KnowledgeAuditChange["action"]>, entry: string) => {
    switch (action) {
      case "update":
        return t(($) => $.detail.memory_action_update, { entry });
      case "merge":
        return t(($) => $.detail.memory_action_merge, { entry });
      case "supersede":
        return t(($) => $.detail.memory_action_supersede, { entry });
      default:
        return t(($) => $.detail.memory_action_new);
    }
  };

  return (
    <section className="rounded-lg border bg-card p-4 shadow-xs" aria-labelledby="project-memory-heading">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div>
          <h3 id="project-memory-heading" className="text-body font-medium">{t(($) => $.detail.memory_title)}</h3>
          <p className="text-caption text-muted-foreground">{t(($) => $.detail.memory_hint)}</p>
        </div>
        <span className="text-caption text-muted-foreground">
          {formatObservedAt(data?.observed_at, t(($) => $.detail.memory_no_check))}
        </span>
      </div>
      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-5 w-full" />)}
        </div>
      ) : (
        <div className="space-y-2">
          {(data?.locations ?? []).map((location) => {
            const behind = !location.exists && location.mainline_ref ? location.mainline_ref : null;
            return (
              <div key={location.key} className="flex items-center gap-2 text-caption">
                {location.exists ? <CheckCircle2 className="size-4 text-emerald-600" /> : <AlertCircle className="size-4 text-amber-600" />}
                <span className="min-w-0 flex-1 truncate" title={location.path}>{location.path}</span>
                <span
                  className={cn("shrink-0", location.exists ? "text-emerald-700" : "text-amber-700")}
                  title={behind ? t(($) => $.detail.memory_behind_title, { ref: behind }) : undefined}
                >
                  {location.exists
                    ? t(($) => $.detail.memory_present)
                    : behind
                      ? t(($) => $.detail.memory_behind, { ref: behind })
                      : t(($) => $.detail.memory_missing)}
                </span>
              </div>
            );
          })}
        </div>
      )}
      {data?.sediment_issue ? (
        <AppLink
          href={workspacePaths.issueDetail ? workspacePaths.issueDetail(data.sediment_issue.id) : `#`}
          className="mt-3 inline-flex text-caption text-primary hover:underline"
        >
          {t(($) => $.detail.memory_ticket, { id: data.sediment_issue.identifier })}
        </AppLink>
      ) : !data?.sediment_agent_configured ? (
        <div className="mt-3 flex flex-wrap items-center gap-1.5 text-caption text-amber-700">
          <span>{data?.sediment_error ? `${data.sediment_error}。` : t(($) => $.detail.memory_unconfigured_error)}</span>
          <AppLink
            href={workspacePaths.settings ? workspacePaths.settings() : "#"}
            className="text-primary hover:underline font-medium"
          >
            {t(($) => $.detail.memory_configure_link)}
          </AppLink>
        </div>
      ) : data?.sediment_error ? (
        <p className="mt-3 text-caption text-amber-700">{data.sediment_error}</p>
      ) : null}
      {data?.recent_sediments && data.recent_sediments.length > 0 ? (
        <div className="mt-3 space-y-1.5" aria-labelledby="project-memory-recent-heading">
          <p id="project-memory-recent-heading" className="text-caption text-muted-foreground">
            {t(($) => $.detail.memory_recent)}
          </p>
          {data.recent_sediments.map((sediment) => {
            const changes = sediment.changes.map((change) => {
              const files = (change.files?.length ? change.files : [change.location]).join(", ");
              const action = change.action ? actionLabel(change.action, change.entry ?? "") : "";
              return action ? `${action} · ${files}` : files;
            });
            const rolledUp = (sediment.sources ?? [])
              .map((src) => src.identifier ?? src.title)
              .filter(Boolean)
              .join(", ");
            const href = sediment.issue_id
              ? workspacePaths.issueDetail?.(sediment.issue_id)
              : sediment.chat_session_id
                ? workspacePaths.chatSession?.(sediment.chat_session_id)
                : undefined;
            const source = sediment.issue_identifier
              ?? t(($) => $.detail.memory_recent_chat, { title: sediment.source_title || sediment.chat_session_id?.slice(0, 8) || "" });
            return (
              <div key={sediment.id} className="text-caption">
                <div className="flex items-baseline gap-2">
                  <AppLink
                    href={href ?? "#"}
                    className="min-w-0 flex-1 truncate text-primary hover:underline"
                    title={sediment.source_title || undefined}
                  >
                    {source}
                  </AppLink>
                  {!sediment.verified ? (
                    <span className="shrink-0 text-amber-700" title={t(($) => $.detail.memory_recent_unverified_title)}>
                      {t(($) => $.detail.memory_recent_unverified)}
                    </span>
                  ) : null}
                  <span className="shrink-0 text-muted-foreground">{timeAgo(sediment.created_at)}</span>
                </div>
                {rolledUp ? (
                  <p className="text-muted-foreground [overflow-wrap:anywhere]">
                    {t(($) => $.detail.memory_recent_from, { sources: rolledUp })}
                  </p>
                ) : null}
                {/* Own wrapping lines: a phone has no hover to read a cut-off path. */}
                {changes.map((line, i) => (
                  <p key={i} className="text-muted-foreground [overflow-wrap:anywhere]">{line}</p>
                ))}
              </div>
            );
          })}
        </div>
      ) : data?.latest_sediment_at ? (
        <p className="mt-3 text-caption text-muted-foreground">
          {t(($) => $.detail.memory_last_sediment, { time: formatObservedAt(data.latest_sediment_at, t(($) => $.detail.memory_no_check)) })}
        </p>
      ) : null}
    </section>
  );
}
