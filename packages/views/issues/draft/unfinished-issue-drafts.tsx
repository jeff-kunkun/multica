"use client";

import { useState } from "react";
import { ChevronRight, MessageSquare } from "lucide-react";
import {
  decodeIssueDraftInput,
  stripIssueDraftDirectives,
} from "@multica/core/issue-drafts";
import type { IssueDraftSummary } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { useT, useTimeAgo } from "../../i18n";

/**
 * Offers the alignments this user started and left, on the one screen that
 * would otherwise silently start another one.
 *
 * A draft's carrier is a `kind='system'` agent, so it is absent from every chat
 * list. This banner is the route back to the ones still IN PROGRESS, and the
 * chat sidebar's alignment records (DENE-371) is the route back to all of them,
 * finished ones included. One unfinished draft opens directly — a chooser
 * listing one item is a question with one answer — and several open a dialog,
 * because picking between them needs to show what each one is about.
 */
export function UnfinishedIssueDraftsBanner({
  drafts,
  onResume,
}: {
  drafts: IssueDraftSummary[];
  onResume: (draftId: string) => void;
}) {
  const { t } = useT("issues");
  const [picking, setPicking] = useState(false);

  if (drafts.length === 0) return null;

  const openOrPick = () => {
    const only = drafts[0];
    if (drafts.length === 1 && only) {
      onResume(only.chat_session_id);
      return;
    }
    setPicking(true);
  };

  return (
    <>
      <button
        type="button"
        onClick={openOrPick}
        className={cn(
          "mb-4 flex w-full items-center gap-3 rounded-lg border bg-muted/40 px-4 py-3 text-left transition-colors",
          "hover:border-primary/40 hover:bg-accent/30",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-background text-muted-foreground">
          <MessageSquare className="size-4" aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1 text-body">
          {t(($) => $.alignment.drafts_banner, { count: drafts.length })}
        </span>
        <ChevronRight
          className="size-4 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
      </button>

      <Dialog open={picking} onOpenChange={setPicking}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t(($) => $.alignment.drafts_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.alignment.drafts_hint)}
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-96 space-y-2 overflow-y-auto">
            {drafts.map((draft) => (
              <button
                key={draft.chat_session_id}
                type="button"
                onClick={() => {
                  setPicking(false);
                  onResume(draft.chat_session_id);
                }}
                className={cn(
                  "flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
                  "hover:border-primary/40 hover:bg-accent/30",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span className="flex items-baseline gap-2">
                    <span className="truncate text-body font-medium">
                      {issueDraftTitle(draft) ||
                        t(($) => $.alignment.drafts_untitled)}
                    </span>
                    {draft.last_message_at ? (
                      <DraftTimestamp at={draft.last_message_at} />
                    ) : null}
                  </span>
                  {/* Two lines: enough to recognise which alignment this is,
                      short enough that a long reply cannot push the next row
                      off the dialog. */}
                  <span className="mt-1 line-clamp-2 block text-caption leading-5 text-muted-foreground">
                    {issueDraftPreview(draft)}
                  </span>
                </span>
              </button>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

function DraftTimestamp({ at }: { at: string }) {
  const timeAgo = useTimeAgo();
  return (
    <span className="ml-auto shrink-0 text-micro text-muted-foreground">
      {timeAgo(at)}
    </span>
  );
}

/**
 * What the user calls this alignment. The stored chat title is the same string
 * on every row ("Align a new issue") and would make the list unreadable, so the
 * draft's own title is the only candidate worth showing.
 */
export function issueDraftTitle(
  draft: Pick<IssueDraftSummary, "draft">,
): string {
  return draft.draft?.title?.trim() ?? "";
}

/**
 * The last thing said, in the user's own words.
 *
 * The stored turn is still in the carrier's wire format — the user side is a
 * JSON envelope carrying the whole draft, the assistant side ends in an
 * `<issue_draft>` block. Shown raw it is a wall of JSON, so both sides are
 * decoded through the same helpers the conversation itself uses.
 */
export function issueDraftPreview(
  draft: Pick<IssueDraftSummary, "last_message_content" | "last_message_role">,
): string {
  const content = draft.last_message_content;
  if (!content) return "";
  return draft.last_message_role === "user"
    ? decodeIssueDraftInput(content)
    : stripIssueDraftDirectives(content);
}
