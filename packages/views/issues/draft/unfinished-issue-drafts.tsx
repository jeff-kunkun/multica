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
 * otherwise silently start another one — the dialog's alignment face and its
 * manual face alike (DENE-443): switching to "New issue" used to hide the one
 * route back to a conversation already in progress.
 *
 * A draft's carrier is a `kind='system'` agent, so it is absent from every chat
 * list. This banner is the route back to the ones still IN PROGRESS, and the
 * chat sidebar's alignment records (DENE-371) is the route back to all of them,
 * finished ones included.
 *
 * The banner always opens the list, one unfinished draft included. It used to
 * resume a lone draft directly — a chooser listing one item is a question with
 * one answer — but the list is now also where an alignment is GIVEN UP on, and
 * one unfinished draft is exactly the case where that matters most: the
 * shortcut would swallow the only way to reach it.
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
  // The row the user asked to discard, held until the confirm answers. Not a
  // boolean plus a second id: one value cannot describe two rows, and the
  // confirm text names the alignment it is about.
  const [discarding, setDiscarding] = useState<IssueDraftSummary | null>(null);
  const abandon = useAbandonIssueDraft(wsId);

  if (drafts.length === 0) return null;

  const closeConfirm = () => {
    setDiscarding(null);
    abandon.reset();
  };

  const confirmDiscard = async () => {
    if (!discarding) return;
    // Awaited so the dialog can state a refusal instead of closing over it.
    // The row leaves the list through the mutation's own invalidate — removing
    // it here would hide a server that said no.
    try {
      await abandon.mutateAsync(discarding.chat_session_id);
    } catch {
      // Stays open; `abandon.error` is rendered below the description.
      return;
    }
    setDiscarding(null);
  };

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
              const name =
                issueDraftTitle(draft) || t(($) => $.alignment.drafts_untitled);
              return (
                <div
                  key={draft.chat_session_id}
                  className={cn(
                    "flex items-start gap-1 rounded-lg border transition-colors",
                    "focus-within:border-primary/40 hover:border-primary/40 hover:bg-accent/30",
                  )}
                >
                  <button
                    type="button"
                    onClick={() => {
                      setPicking(false);
                      onResume(draft.chat_session_id);
                    }}
                    className={cn(
                      "flex min-w-0 flex-1 items-start gap-3 p-3 text-left",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:rounded-lg",
                    )}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="flex items-baseline gap-2">
                        <span className="truncate text-body font-medium">{name}</span>
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
                  {/* Sibling of the resume button, never nested inside it: the
                      row is the primary action and a nested button is invalid
                      DOM. `stopPropagation` still guards the shared hover
                      surface from a future wrapper handler. */}
                  <button
                    type="button"
                    aria-label={t(($) => $.alignment.drafts_discard_aria, { title: name })}
                    title={t(($) => $.alignment.drafts_discard_aria, { title: name })}
                    onClick={(event) => {
                      event.stopPropagation();
                      setDiscarding(draft);
                    }}
                    className={cn(
                      "mr-2 mt-2 flex shrink-0 items-center rounded-sm p-2 text-muted-foreground transition-colors",
                      "hover:bg-accent hover:text-destructive",
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
        open={discarding !== null}
        onOpenChange={(open) => {
          if (!open) closeConfirm();
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.alignment.drafts_discard_title)}
            </AlertDialogTitle>
            {/* Says what the server actually does: this is an ABANDON, not a
                deletion. The conversation stays readable in the alignment
                records and any issue an earlier round already created is
                untouched — a "delete" that quietly meant either would be a
                promise the backend never made. */}
            <AlertDialogDescription>
              {t(($) => $.alignment.drafts_discard_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {abandon.isError ? (
            <p role="alert" className="text-body text-destructive">
              {clientErrorMessage(abandon.error) ??
                t(($) => $.alignment.abandon_failed)}
            </p>
          ) : null}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={abandon.isPending}>
              {t(($) => $.alignment.drafts_discard_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={abandon.isPending}
              onClick={(event) => {
                // The confirm must stay open on a refusal, so closing is this
                // handler's own decision rather than the primitive's default.
                event.preventDefault();
                void confirmDiscard();
              }}
            >
              {abandon.isPending
                ? t(($) => $.alignment.abandoning)
                : t(($) => $.alignment.drafts_discard_confirm)}
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
