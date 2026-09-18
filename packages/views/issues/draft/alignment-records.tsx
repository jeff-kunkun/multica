"use client";

import { useQuery } from "@tanstack/react-query";
import { MessageSquare } from "lucide-react";
import { useWorkspacePaths } from "@multica/core/paths";
import { issueDraftListOptions } from "@multica/core/issue-drafts";
import type { IssueDraftStatus, IssueDraftSummary } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";
import { issueDraftPreview, issueDraftTitle } from "./unfinished-issue-drafts";

/**
 * The alignments this user has had, as a group in the chat sidebar.
 *
 * A GROUP rather than rows mixed into the conversation list, and that is not a
 * visual preference. Alignment conversations run on a hidden `kind='system'`
 * carrier, and `GET /api/chat/sessions` drops every session whose agent is not
 * a `kind='user'` one — the same access boundary that keeps private agents out
 * of the list. Mixing them in would mean widening that boundary and then
 * teaching every ordinary row's actions (pin, archive, delete, stop, typing)
 * about a case they have no business having. A group reads the one endpoint
 * that is already scoped to this user's own drafts and leaves the chat list
 * exactly as it was.
 *
 * Finished alignments are here on purpose: a confirmed one is the only record
 * of what was agreed before the issue existed, and an abandoned one is the only
 * record of what was considered and dropped. Neither is reachable anywhere else
 * — no chat surface lists these conversations. (DENE-371)
 */
export function AlignmentRecords({ wsId }: { wsId: string }) {
  const { t } = useT("issues");
  const { data: drafts = [] } = useQuery(issueDraftListOptions(wsId));

  // Nothing to show is the common case for someone who has never aligned:
  // an empty section would be permanent furniture describing an absence.
  if (drafts.length === 0) return null;

  return (
    <section aria-label={t(($) => $.alignment.records_title)} className="mt-1 border-t pt-1">
      <h2 className="flex items-center gap-2 px-2 py-1.5 text-caption font-medium uppercase tracking-wide text-muted-foreground">
        <MessageSquare className="size-3.5 shrink-0" aria-hidden="true" />
        {t(($) => $.alignment.records_title)}
      </h2>
      <ul>
        {drafts.map((draft) => (
          <AlignmentRecordRow key={draft.chat_session_id} draft={draft} />
        ))}
      </ul>
    </section>
  );
}

function AlignmentRecordRow({ draft }: { draft: IssueDraftSummary }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const timeAgo = useTimeAgo();
  const title = issueDraftTitle(draft) || t(($) => $.alignment.drafts_untitled);
  const preview = issueDraftPreview(draft);
  const at = draft.last_message_at || draft.updated_at;

  return (
    <li>
      <AppLink
        href={paths.newIssueDraft(draft.chat_session_id)}
        className={cn(
          "block rounded-md px-2 py-2 transition-colors",
          "hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        <span className="flex items-baseline gap-2">
          <span className="min-w-0 flex-1 truncate text-body">{title}</span>
          <span className="shrink-0 text-micro text-muted-foreground">
            {at ? timeAgo(at) : null}
          </span>
        </span>
        <span className="mt-0.5 flex items-center gap-2">
          <StatusTag status={draft.status} />
          {preview ? (
            <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground">
              {preview}
            </span>
          ) : null}
        </span>
      </AppLink>
    </li>
  );
}

/**
 * Which kind of record this is. `draft` and `ready` share one label on purpose:
 * the difference between them is whether the draft is confirmable, which is a
 * decision for the alignment page, not something a sidebar row has to explain.
 */
function StatusTag({ status }: { status: IssueDraftStatus }) {
  const { t } = useT("issues");
  const label =
    status === "completed"
      ? t(($) => $.alignment.records_status_completed)
      : status === "abandoned"
        ? t(($) => $.alignment.records_status_abandoned)
        : t(($) => $.alignment.records_status_open);
  return (
    <span
      className={cn(
        "shrink-0 rounded-full px-1.5 py-0.5 text-micro",
        status === "completed"
          ? "bg-success/10 text-success"
          : status === "abandoned"
            ? "bg-muted text-muted-foreground"
            : "bg-primary/10 text-primary",
      )}
    >
      {label}
    </span>
  );
}
