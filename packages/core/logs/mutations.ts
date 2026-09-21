import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { TaskLogExportProgress } from "../api/client";
import { issueKeys } from "../issues/queries";
import { buildLogExportReportComment, resolveLogExportMention } from "./report";
import type {
  Comment,
  TaskLogExport,
  TaskLogExportPush,
  TaskLogExportReport,
  TaskLogExportScope,
} from "../types";

export interface ExportTaskLogsVars {
  taskId: string;
  scope: TaskLogExportScope;
  /** Only meaningful with `scope: "hours"`. */
  hours?: number;
  /** Live readout of the streamed artifact; see `api.exportTaskLogs`. */
  onProgress?: (progress: TaskLogExportProgress) => void;
}

/**
 * Fetch a log export bundle for a run.
 *
 * A mutation rather than a query: an export is a document the reader asks for
 * once, at a chosen scope, and `generated_at` moves on every call. Caching it
 * as server state would either serve a stale artifact or refetch a
 * multi-megabyte body behind the user's back.
 */
export function useExportTaskLogs() {
  return useMutation({
    mutationFn: (vars: ExportTaskLogsVars): Promise<TaskLogExport> =>
      api.exportTaskLogs(vars.taskId, {
        scope: vars.scope,
        hours: vars.hours,
        onProgress: vars.onProgress,
      }),
  });
}

export interface ReportTaskLogExportVars {
  /** The fetched export. Its `artifact` is uploaded verbatim on the fallback. */
  exported: TaskLogExport;
  /** The issue the export is reported on — normally `exported.bundle.task.issue_id`. */
  issueId: string;
  /** The run being reported; defaults to the bundle's own `task.id`. */
  taskId?: string;
  /** Ready-made mention link, e.g. from `logExportOwnerMention`. */
  mention?: string;
  /** The scope the export was fetched with, so the push rebuilds the same document. */
  scope?: TaskLogExportScope;
  hours?: number;
  /**
   * `"attachment"` skips the workspace git repository and always uploads the
   * artifact, which is what the dialog uses when the operator asks for the
   * old behavior. Defaults to `"auto"`: try the repository, fall back to the
   * attachment.
   */
  channel?: "auto" | "attachment";
}

/**
 * Report a fetched log export on its issue.
 *
 * Two channels, in order:
 *
 *  1. The workspace's configured log repository. The server rebuilds the
 *     bundle with the same generator and commits it, so the comment carries a
 *     link instead of a multi-megabyte attachment — the upload path on this
 *     instance dies on large bodies long before GitHub would.
 *  2. The ordinary comment attachment, composed from the two calls the normal
 *     comment composer already makes (`uploadFile`, then `createComment` with
 *     the attachment). The fetched `artifact` is uploaded byte for byte; it is
 *     never re-encoded from the parsed bundle.
 *
 * Falling back is not an error path: a workspace with no repository configured
 * reports exactly the way it did before the repository existed. The reason is
 * returned so the dialog can say which way it went.
 */
export function useReportTaskLogExport() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (vars: ReportTaskLogExportVars): Promise<TaskLogExportReport> => {
      // Resolved here rather than required from the caller: the mention rule
      // needs the issue's assignee/creator, and the surfaces that open the
      // dialog (a run row, a transcript) do not all have the issue in hand.
      const mention = vars.mention ?? (await resolveMention(vars.issueId));
      const report = { ...vars, mention };
      let fallbackReason: string | undefined;

      if (vars.channel !== "attachment") {
        try {
          const push = await api.pushTaskLogExport(
            vars.taskId || vars.exported.bundle.task.id,
            {
              scope: vars.scope ?? "run",
              hours: vars.hours,
            },
          );
          return await reportViaRepository(report, push);
        } catch (error) {
          fallbackReason = error instanceof Error ? error.message : String(error);
        }
      }

      return reportViaAttachment(report, fallbackReason);
    },
    onSuccess: (_report, vars) => {
      // The comment lands in the issue timeline; refresh it so the reported
      // attachment shows without a manual reload.
      queryClient.invalidateQueries({ queryKey: issueKeys.timeline(vars.issueId) });
    },
  });
}

/**
 * Resolve the default mention for an issue, or `undefined` for nobody.
 *
 * A lookup failure is not a report failure: the comment still goes out, just
 * without a mention. Guessing a target would be worse than silence — the one
 * thing the rule exists to prevent is waking an agent that just died.
 */
async function resolveMention(issueId: string): Promise<string | undefined> {
  try {
    return resolveLogExportMention(await api.getIssue(issueId));
  } catch {
    return undefined;
  }
}

/** Comment body for a bundle that was committed to the workspace repository. */
async function reportViaRepository(
  vars: ReportTaskLogExportVars,
  push: TaskLogExportPush,
): Promise<TaskLogExportReport> {
  const comment = await api.createComment(
    vars.issueId,
    buildLogExportReportComment(
      { task: vars.exported.bundle.task, summary_markdown: push.summary_markdown },
      vars.mention,
      push.url,
    ),
  );
  return { channel: "git", issueId: vars.issueId, commentId: comment.id, url: push.url };
}

/** Comment body carrying the artifact itself, as before c4. */
async function reportViaAttachment(
  vars: ReportTaskLogExportVars,
  fallbackReason?: string,
): Promise<TaskLogExportReport> {
  const file = new File([vars.exported.artifact], vars.exported.filename, {
    type: "application/json",
  });
  const attachment = await api.uploadFile(file, { issueId: vars.issueId });
  if (!attachment.id) {
    throw new Error("Upload did not return an attachment id");
  }
  const comment: Comment = await api.createComment(
    vars.issueId,
    buildLogExportReportComment(vars.exported, vars.mention),
    undefined,
    undefined,
    [attachment.id],
  );
  return {
    channel: "attachment",
    issueId: vars.issueId,
    commentId: comment.id,
    fallbackReason,
  };
}
