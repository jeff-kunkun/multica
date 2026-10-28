"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { useFeatureEnabled } from "@multica/core/config";
import { OFFICE_PROJECT_FOLDER_FLAG } from "@multica/core/feature-flags";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions } from "@multica/core/issues";
import { useWorkspacePaths } from "@multica/core/paths";
import { issueSharedFolders, type DaemonOfRuntime } from "@multica/core/projects";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import type { AgentTask } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

/** The workspace's dispatched and running tasks, and which machine each runtime is on. */
export function useFolderRunsSnapshot(): { tasks: AgentTask[]; daemonOf: DaemonOfRuntime } {
  const wsId = useWorkspaceId();
  const { data: tasks = [] } = useQuery({ ...agentTaskSnapshotOptions(wsId), enabled: !!wsId });
  const { data: runtimes = [] } = useQuery({ ...runtimeListOptions(wsId), enabled: !!wsId });
  const daemonOf = useMemo(() => {
    const byRuntime = new Map(runtimes.map((runtime) => [runtime.id, runtime.daemon_id]));
    return (runtimeId: string) => byRuntime.get(runtimeId);
  }, [runtimes]);
  return { tasks, daemonOf };
}

/**
 * Several runs in one folder at once. Nothing is blocked — the daemon lets
 * them share it — so this only tells people who else is editing those files.
 */
export function FolderRunsNotice({
  runs,
  title,
  currentIssueId,
}: {
  runs: AgentTask[];
  title?: string;
  currentIssueId?: string;
}) {
  const { t } = useT("projects");
  if (runs.length < 2) return null;
  return (
    <div
      role="status"
      className="space-y-1 rounded-md border border-amber-500/40 bg-amber-500/5 px-2.5 py-2 text-caption"
    >
      <div className="flex items-start gap-1.5 text-amber-700 dark:text-amber-400">
        <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
        <div className="min-w-0">
          {title ? <div className="truncate font-medium">{title}</div> : null}
          <p>{t(($) => $.folder.shared, { count: runs.length })}</p>
        </div>
      </div>
      <ul className="space-y-0.5 pl-5">
        {runs.map((run) => (
          <FolderRunRow key={run.id} run={run} current={!!currentIssueId && run.issue_id === currentIssueId} />
        ))}
      </ul>
    </div>
  );
}

function FolderRunRow({ run, current }: { run: AgentTask; current: boolean }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { getActorName } = useActorName();
  const { data: issue } = useQuery({
    ...issueDetailOptions(wsId, run.issue_id ?? ""),
    enabled: !!run.issue_id && !current,
  });

  return (
    <li className="flex min-w-0 items-center gap-1.5">
      <span className="shrink-0 font-medium">{getActorName("agent", run.agent_id)}</span>
      {current ? (
        <span className="text-muted-foreground">{t(($) => $.folder.this_issue)}</span>
      ) : run.issue_id ? (
        <AppLink
          href={paths.issueDetail(run.issue_id)}
          className="min-w-0 truncate text-brand hover:underline"
          title={issue ? `${issue.identifier} ${issue.title}` : undefined}
        >
          {issue ? (
            <>
              <span className="mr-1 font-mono text-micro">{issue.identifier}</span>
              {issue.title}
            </>
          ) : (
            "…"
          )}
        </AppLink>
      ) : run.chat_session_id ? (
        <AppLink href={paths.chatSession(run.chat_session_id)} className="text-brand hover:underline">
          {t(($) => $.folder.chat_run)}
        </AppLink>
      ) : null}
    </li>
  );
}

/** On an issue: every folder this issue's runs share with another run. */
export function IssueSharedFolderNotice({ issueId }: { issueId: string }) {
  const enabled = useFeatureEnabled(OFFICE_PROJECT_FOLDER_FLAG);
  return enabled && issueId ? <IssueSharedFolders issueId={issueId} /> : null;
}

function IssueSharedFolders({ issueId }: { issueId: string }) {
  const { t } = useT("projects");
  const { tasks, daemonOf } = useFolderRunsSnapshot();
  const groups = useMemo(() => issueSharedFolders(tasks, daemonOf, issueId), [tasks, daemonOf, issueId]);
  if (groups.length === 0) return null;
  return (
    <div className="mt-3 space-y-2">
      {groups.map(({ folder, runs }) => (
        <FolderRunsNotice
          key={`${folder.machine}:${folder.path}`}
          runs={runs}
          // Only the folder's own name: the full path carries the account name.
          title={t(($) => $.folder.shared_folder, { name: folder.path.split("/").pop() || folder.path })}
          currentIssueId={issueId}
        />
      ))}
    </div>
  );
}
