"use client";

import { useState } from "react";
import { MessageSquare, RefreshCw } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { planIssueDraftGroupProgress, useReopenIssueDraft } from "@multica/core/issue-drafts";
import { issueBehavesAs } from "@multica/core/issues";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";

/**
 * The alignment entry on an issue detail page: where this group came from, how
 * far it has got, and the way back into the SAME conversation (DENE-415).
 *
 * It sits beside the "sub-issue of" line because it answers the same kind of
 * question — where does this come from — and because an alignment conversation
 * is reachable from nowhere else: its carrier is a hidden system agent, so it
 * appears in no chat list. DENE-371 put the read-only "view the alignment" link
 * here; this adds the two things a person with a changed requirement needs: the
 * group's progress, and one action that returns them to the conversation that
 * produced it rather than to the issue form.
 *
 * The action is a REOPEN, not a new alignment: the round continues on the same
 * chat session and the same draft row, so the next confirm appends to this
 * group instead of founding another one. The server treats that as idempotent —
 * a draft that is still open is answered as it stands — so double-clicking
 * costs a request, not a second round.
 */
export function IssueAlignmentEntry({
  draftId,
  groupIssues,
}: {
  /** The alignment conversation this group came out of. */
  draftId: string;
  /**
   * The group as it stands: the root plus its children, whichever member of it
   * the page is looking at. The progress line is about the whole group, not
   * about the issue that happens to be open.
   */
  groupIssues: Issue[];
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const reopen = useReopenIssueDraft(wsId);
  const [failed, setFailed] = useState(false);

  const progress = planIssueDraftGroupProgress(groupIssues, (issue) =>
    issueBehavesAs(issue, "done"),
  );

  const continueAligning = () => {
    setFailed(false);
    void reopen
      .mutateAsync(draftId)
      .then(() => navigation.push(paths.newIssueDraft(draftId)))
      .catch(() => {
        // Only reachable when the conversation is gone — a node that outlived
        // its alignment, or a link from another workspace. Saying so beats a
        // navigation to a page that can only redirect back.
        setFailed(true);
      });
  };

  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5">
      <AppLink
        href={paths.newIssueDraft(draftId)}
        className="inline-flex max-w-full items-center gap-1.5 text-caption text-muted-foreground hover:text-foreground transition-colors"
      >
        <MessageSquare className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span className="truncate">{t(($) => $.detail.alignment_origin)}</span>
      </AppLink>

      {/* What the group is doing, in the same shape the group's own surfaces
          use: how many issues, how many finished, and which stage is being
          worked on. An alignment that produced one issue has no group to
          report on, and "1 issue · 0 done" beside the link is noise, so the
          line is about groups of more than one. */}
      {progress.total > 1 ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.detail.alignment_group_progress, {
            count: progress.total,
            done: progress.done,
          })}
          {progress.stages > 0
            ? ` · ${t(($) => $.detail.alignment_group_stage, {
                stage: progress.activeStage ?? progress.stages,
                total: progress.stages,
              })}`
            : ""}
        </span>
      ) : null}

      <Button
        variant="outline"
        size="sm"
        className="h-6 gap-1 px-2 text-caption"
        disabled={reopen.isPending}
        onClick={continueAligning}
      >
        <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
        {reopen.isPending
          ? t(($) => $.detail.alignment_continue_pending)
          : t(($) => $.detail.alignment_continue)}
      </Button>

      {failed ? (
        <span role="alert" className="text-caption text-destructive">
          {t(($) => $.detail.alignment_continue_failed)}
        </span>
      ) : null}
    </div>
  );
}
