"use client";

import { useQuery } from "@tanstack/react-query";
import { projectResourcesOptions } from "@multica/core/projects";
import { resolveTaskCodeSource } from "@multica/core/projects/source-rule";
import { issueDetailOptions } from "@multica/core/issues";
import type { AgentTask, LocalDirectoryResourceRef, ProjectResource } from "@multica/core/types";
import { localDirectoryLabel } from "../../projects/components/local-directory-label";
import { useT } from "../../i18n";

/**
 * "Where did this run's code come from?" — source type, the directory on the
 * machine, the execution mode, and which resource decided it.
 *
 * Before this, a run showed a work dir and nothing that explained it, so a
 * project configured with both a remote repo and a local directory gave no
 * way to tell which one the run had actually used — and it was usually the
 * wrong one (DENE-595).
 *
 * Rendered as rows inside the existing run-info popover rather than as a chip:
 * the answer is four facts, and three of them only mean something together.
 */
export interface TaskSourceRowsProps {
  wsId: string;
  /** The machine this run is bound to, from its runtime. Null when unknown. */
  daemonId: string | null;
  task: AgentTask;
  /** Renderer supplied by the host popover so the rows match their siblings. */
  renderRow: (row: { label: string; value: string; mono?: boolean }) => React.ReactNode;
}

export function TaskSourceRows({ wsId, daemonId, task, renderRow }: TaskSourceRowsProps) {
  const { t } = useT("agents");
  // The project is what carries the source configuration, and a task only
  // reaches it through its issue. Both queries are normally already warm —
  // this popover opens from a surface that listed the issue.
  const { data: issue } = useQuery({
    ...issueDetailOptions(wsId, task.issue_id),
    enabled: !!task.issue_id,
  });
  const projectId = issue?.project_id ?? null;
  const { data: resources } = useQuery({
    ...projectResourcesOptions(wsId, projectId ?? ""),
    enabled: !!projectId,
  });

  if (!projectId) return null;
  // Absent data is not "no local directory": saying "remote checkout" while the
  // list is still loading would state a fact the UI has not established.
  if (!resources) return null;

  const source = resolveTaskCodeSource({
    resources,
    daemonId,
    hasReportedWorkDir: !!task.work_dir,
  });

  if (source.kind !== "local_directory") {
    return <>{renderRow({
      label: t(($) => $.transcript.details_source),
      value: t(($) => $.transcript.source_remote_checkout),
    })}</>;
  }

  const modeLabel =
    source.executionMode === "worktree"
      ? t(($) => $.transcript.source_mode_worktree)
      : source.executionMode === "shared"
        ? t(($) => $.transcript.source_mode_shared)
        : t(($) => $.transcript.source_mode_in_place);

  // Before the daemon reports a work dir there is nothing to confirm, so the
  // path is labelled as what WILL be used rather than what was.
  const pathLabel = source.predicted
    ? t(($) => $.transcript.details_source_path_planned)
    : t(($) => $.transcript.details_source_path);

  const resourceName = source.resource
    ? localDirectoryLabel(
        source.resource as ProjectResource & { resource_ref: LocalDirectoryResourceRef },
      )
    : "";

  return (
    <>
      {renderRow({
        label: t(($) => $.transcript.details_source),
        value: t(($) => $.transcript.source_local_directory),
      })}
      {source.localPath &&
        renderRow({ label: pathLabel, value: source.localPath, mono: true })}
      {renderRow({ label: t(($) => $.transcript.details_source_mode), value: modeLabel })}
      {resourceName &&
        renderRow({ label: t(($) => $.transcript.details_source_resource), value: resourceName })}
    </>
  );
}
