"use client";

import { useState } from "react";
import { ChevronRight, MessageSquare, Trash2 } from "lucide-react";
import { clientErrorMessage } from "@multica/core/api";
import {
  decodeIssueDraftInput,
  stripIssueDraftDirectives,
  useAbandonIssueDraft,
} from "@multica/core/issue-drafts";
import type { IssueDraftSummary } from "@multica/core/types";
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
 * Offers the alignments this user started and left, on the screens that would
 * otherwise silently start another one.
 *
 * A draft's carrier is a `kind='system'` agent, so it is absent from every chat
 * list. This banner is the route back to the ones still IN PROGRESS, and the
 * chat sidebar's alignment records (DENE-371) is the route back to all of them,
 * finished ones included.
 *
 * It always opens the list, even when there is a single row. The one-row
 * shortcut assumed the only thing a row could do was resume, so asking a
 * question with one answer would have been noise; a row is also where an
 * alignment the user is done with gets discarded, and the shortcut would have
 * taken that action away from exactly the case that needs it first (DENE-443).
 *
 * Discarding is abandon, never a physical delete: the row leaves this list, the
 * conversation stays in the alignment records, and whatever the alignment
 * already created is untouched. The confirmation says so, because "discard"
 * alone reads like the alignment is about to be erased.
 */
export function UnfinishedIssueDraftsBanner({
  wsId,
  drafts,
  onResume,
}: {
  wsId: string;
  drafts: IssueDraftSummary[];
  onResume: (draftId: string) => void;
}) {
  const { t } = useT("issues");
  const [picking, setPicking] = useState(false);
  // The row the confirmation is about, not a boolean: the copy and the write
  // both need to name it, and a second row opened mid-flight must not inherit
  // the first one's request.
  const [confirming, setConfirming] = useState<IssueDraftSummary | null>(null);
  const abandon = useAbandonIssueDraft(wsId);

  if (drafts.length === 0) return null;

  // A refusal belongs to the row that caused it: opening the confirmation for
  // another row starts from a clean mutation, or the previous message would
  // stand over an alignment it never mentioned.
  const askToDiscard = (draft: IssueDraftSummary) => {
    abandon.reset();
    setConfirming(draft);
  };

  const dismissConfirm = () => {
    abandon.reset();
    setConfirming(null);
  };

  // Closes only on success. The row disappearing is the server's answer, and
  // the mutation's own invalidation is what removes it — reaching zero rows
  // takes the whole banner, and this dialog with it. A refusal keeps the
  // confirmation up, because that is where its reason is rendered.
  const discard = async () => {
    if (!confirming) return;
    try {
      await abandon.mutateAsync(confirming.chat_session_id);
      setConfirming(null);
    } catch {
      // Rendered below from the mutation's own error; nothing to add here.
    }
  };

  // The server writes 4xx messages for the reader ("draft is already
  // abandoned"), so they are worth showing verbatim; anything else falls back
  // to the localized sentence rather than leaking a 5xx body (DENE-422).
  const discardFailure = abandon.isError
    ? clientErrorMessage(abandon.error) ?? t(($) => $.alignment.abandon_failed)
    : null;

  return (
    <>
      <button
        type="button"
        onClick={() => setPicking(true)}
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
            {drafts.map((draft) => {
              const title =
                issueDraftTitle(draft) ||
                t(($) => $.alignment.drafts_untitled);
              return (
                // Two siblings, never the discard trigger nested in the resume
                // button: the row resumes from anywhere except the trailing
                // action, and each control keeps its own accessible name.
                <div
                  key={draft.chat_session_id}
                  className={cn(
                    "flex items-start gap-1 rounded-lg border transition-colors",
                    "hover:border-primary/40 hover:bg-accent/30",
                  )}
                >
                  <button
                    type="button"
                    onClick={() => {
                      setPicking(false);
                      onResume(draft.chat_session_id);
                    }}
                    className={cn(
                      "flex min-w-0 flex-1 items-start gap-3 rounded-lg p-3 text-left",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    )}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="flex items-baseline gap-2">
                        <span className="truncate text-body font-medium">
                          {title}
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
                  <button
                    type="button"
                    aria-label={t(($) => $.alignment.drafts_discard_aria, {
                      title,
                    })}
                    onClick={(event) => {
                      // Defensive: the two controls are siblings today, but the
                      // row is the nearest thing either of them is inside, and
                      // discarding must never be read as "continue".
                      event.stopPropagation();
                      askToDiscard(draft);
                    }}
                    className={cn(
                      "m-2 shrink-0 rounded-sm p-1.5 text-muted-foreground transition-colors",
                      "hover:bg-background hover:text-destructive",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    )}
                  >
                    <Trash2 className="size-4" aria-hidden="true" />
                  </button>
                </div>
              );
            })}
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) dismissConfirm();
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.alignment.abandon_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.alignment.drafts_discard_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {discardFailure ? (
            <p role="alert" className="text-body text-destructive">
              {discardFailure}
            </p>
          ) : null}
          <AlertDialogFooter>
            {/* Cancel needs no handler of its own: closing runs
                `dismissConfirm` above, which is also what drops a refusal's
                message. */}
            <AlertDialogCancel disabled={abandon.isPending}>
              {t(($) => $.alignment.abandon_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={abandon.isPending}
              onClick={(event) => {
                // Keep the dialog up until the server has accepted; `discard`
                // closes it on success and leaves it standing on a refusal.
                event.preventDefault();
                void discard();
              }}
            >
              {abandon.isPending
                ? t(($) => $.alignment.abandoning)
                : t(($) => $.alignment.confirm_abandon)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
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
