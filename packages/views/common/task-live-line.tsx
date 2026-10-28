"use client";

import type { ReactNode } from "react";
import { useTaskLive } from "@multica/core/agents";
import { useFeatureEnabled } from "@multica/core/config";
import { OFFICE_LIVE_STATUS_FLAG } from "@multica/core/feature-flags";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { AgentTask, TaskLive } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { formatDuration } from "../dashboard/utils";
import { useT } from "../i18n";

type LiveTask = Pick<AgentTask, "id" | "status" | "issue_id">;

/**
 * Whether a row shows its run's live status (DENE-2667 PR 4): office test builds
 * only, and only for a running issue run. Mount TaskLiveLine only when this is
 * true, so rows without it read nothing.
 */
export function useShowRunLive(task: LiveTask): boolean {
  return useFeatureEnabled(OFFICE_LIVE_STATUS_FLAG) && task.status === "running" && !!task.issue_id;
}

/**
 * The run's live view as its daemon last reported it: how long the run has
 * been quiet and its command in flight, then its last output line. Renders
 * nothing until a report exists. `details` lays it out as labelled rows for
 * cards with room for them (PR 13); narrow lists keep the two-line form.
 */
export function TaskLiveLine({
  task,
  details = false,
  className,
}: {
  task: Pick<LiveTask, "id">;
  details?: boolean;
  className?: string;
}) {
  const wsId = useCurrentWorkspace()?.id;
  const live = useTaskLive(wsId, task.id, true);
  if (!live) return null;
  return details ? <TaskLiveDetails live={live} className={className} /> : <TaskLiveView live={live} className={className} />;
}

/**
 * The live view as labelled rows: the command in flight, the last output
 * line and how long the run has been quiet. Rows share the agent live card's
 * label column, so the card and these rows line up.
 */
export function TaskLiveDetails({ live, className }: { live: TaskLive; className?: string }) {
  const { t } = useT("issues");
  const command = live.command || live.tool;
  return (
    <dl
      className={cn("flex min-w-0 flex-col gap-1.5 text-caption", className)}
      aria-label={t(($) => $.execution_log.live_label)}
      data-testid="task-live-details"
    >
      <LiveRow label={t(($) => $.execution_log.live_command_label)}>
        {command ? (
          <span className={LIVE_VALUE_CLASS} title={command}>
            {command}
          </span>
        ) : (
          <span className="text-muted-foreground">
            {live.tools_in_flight > 0
              ? t(($) => $.execution_log.live_tool_running)
              : t(($) => $.execution_log.live_no_command)}
          </span>
        )}
      </LiveRow>
      <LiveRow label={t(($) => $.execution_log.live_output_label)}>
        {live.last_output ? (
          <span className={LIVE_VALUE_CLASS} title={live.last_output}>
            {live.last_output}
          </span>
        ) : (
          <span className="text-muted-foreground">{t(($) => $.execution_log.live_no_output)}</span>
        )}
      </LiveRow>
      <LiveRow label={t(($) => $.execution_log.live_quiet_label)}>
        <span className="tabular-nums">
          {formatDuration(live.idle_seconds, t(($) => $.execution_log.live_quiet_under_second))}
        </span>
      </LiveRow>
    </dl>
  );
}

/**
 * Long values clamp to two lines only on wide screens with a mouse, where the
 * hover title shows the rest. Touch and narrow screens cannot hover, so they
 * get the whole text, which also keeps it selectable for copying.
 */
const LIVE_VALUE_CLASS = "font-mono wrap-anywhere sm:pointer-fine:line-clamp-2";

function LiveRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-1.5">
      <dt className="w-16 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 text-foreground">{children}</dd>
    </div>
  );
}

export function TaskLiveView({ live, className }: { live: TaskLive; className?: string }) {
  const { t } = useT("issues");
  const quiet = t(($) => $.execution_log.live_quiet, {
    duration: formatDuration(live.idle_seconds, t(($) => $.execution_log.live_quiet_under_second)),
  });
  const command = live.command
    ? t(($) => $.execution_log.live_running, { command: live.command })
    : live.tools_in_flight > 0
      ? null
      : t(($) => $.execution_log.live_no_command);
  return (
    <div
      className={cn("min-w-0 space-y-0.5 text-caption text-muted-foreground", className)}
      aria-label={t(($) => $.execution_log.live_label)}
      data-testid="task-live-line"
    >
      <p className="truncate" title={live.command || undefined}>
        <span className="tabular-nums">{quiet}</span>
        {command && (
          <>
            <span aria-hidden="true"> · </span>
            <span className={live.command ? "font-mono" : undefined}>{command}</span>
          </>
        )}
      </p>
      {live.last_output && (
        <p className="truncate font-mono" title={live.last_output}>
          {t(($) => $.execution_log.live_last_output, { line: live.last_output })}
        </p>
      )}
    </div>
  );
}
