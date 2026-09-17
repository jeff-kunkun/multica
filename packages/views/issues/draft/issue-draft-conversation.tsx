"use client";

import { MessageSquare, Sparkles, TriangleAlert } from "lucide-react";
import type { IssueDraftQuestion } from "@multica/core/issue-drafts";
import type { ChatMessage } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { ChatInput } from "../../chat/components/chat-input";
import {
  ChatMessageList,
  ChatMessageSkeleton,
} from "../../chat/components/chat-message-list";
import { useT } from "../../i18n";

/**
 * The conversation column of an alignment page.
 *
 * The composer is deliberately the chat composer, not a form: the whole point
 * of aligning before creating is that the user answers questions in their own
 * words, and the transport (queueing, cancelling, attachments, draft restore)
 * is the same one every other conversation uses.
 */
export function IssueDraftConversation({
  draftId,
  messages,
  loading,
  pendingTask,
  runtimeOnline,
  sending,
  onSend,
  onStop,
  error,
  question,
  seedFailure,
  readOnly: readOnlyProp,
  transformContent,
}: {
  draftId: string;
  messages: ChatMessage[];
  loading: boolean;
  pendingTask:
    | { task_id?: string; status?: string; created_at?: string }
    | undefined;
  runtimeOnline: boolean;
  sending: boolean;
  /** `commitInput` is the composer's clear; the owner runs it as soon as the
   *  server accepts the message. `attachmentIds` are already-uploaded ids the
   *  composer hands down — a request that starts life as a screenshot or a
   *  spec file has to be alignable without a second upload surface. */
  onSend: (
    content: string,
    attachmentIds?: string[],
    commitInput?: () => void,
  ) => Promise<boolean>;
  onStop: () => void;
  error: string | null;
  /** The question the guided policy is waiting on, if any. */
  question: IssueDraftQuestion | null;
  /**
   * Why this conversation's first turn never reached the carrier, as the entry
   * dialog recorded it, or null. "" is a lost turn whose failure carried no
   * message of its own (a 5xx or a transport error). Rendered in place of the
   * ordinary empty state, which is the state it is otherwise indistinguishable
   * from — and the only place the user can learn that the request they typed
   * was never asked (DENE-425).
   */
  seedFailure: string | null;
  /**
   * Render the transcript and nothing else — the shape a FINISHED alignment is
   * read back in (DENE-371). The composer, the answer chips and the runtime
   * badge all describe a next turn, and a record has none: the server refuses a
   * save or a turn against a terminal draft, so offering them would promise
   * work that cannot happen.
   */
  readOnly?: boolean;
  /**
   * How to render a settled assistant turn. The list draws those from the
   * carrier's task transcript, whose text is the wire format verbatim, so the
   * owner has to hand down the same strip it applies to the message bodies.
   */
  transformContent: (content: string) => string;
}) {
  const { t } = useT("issues");
  const pending = !!pendingTask?.task_id;
  const draftKey = `issue-draft:${draftId}`;
  const agentName = t(($) => $.alignment.conversation_title);
  const readOnly = readOnlyProp === true;

  return (
    // `@container`: this is one column of a split layout, so the shared chat
    // gutter must size against the column, not the viewport.
    <section className="flex h-full min-h-0 flex-col bg-background @container">
      <header className="flex min-h-14 shrink-0 items-center justify-between gap-4 border-b px-5 py-2.5">
        <div className="min-w-0">
          <h2 className="truncate text-body font-semibold">
            {t(($) => $.alignment.conversation_title)}
          </h2>
          <p className="truncate text-caption text-muted-foreground">
            {t(($) => $.alignment.conversation_hint)}
          </p>
        </div>
        {!readOnly ? (
          <div className="flex shrink-0 items-center gap-1.5 text-caption text-muted-foreground">
            <span
              className={cn(
                "size-2 rounded-full",
                runtimeOnline ? "bg-success" : "bg-muted-foreground/40",
              )}
              aria-hidden="true"
            />
            {runtimeOnline
              ? t(($) => $.alignment.runtime_online)
              : t(($) => $.alignment.runtime_offline)}
          </div>
        ) : null}
      </header>

      {loading ? (
        <ChatMessageSkeleton />
      ) : messages.length > 0 || pending ? (
        <ChatMessageList
          messages={messages}
          pendingTask={pendingTask}
          availability={runtimeOnline ? "online" : "offline"}
          transformContent={transformContent}
        />
      ) : (
        <div className="flex min-h-0 flex-1 items-center justify-center overflow-y-auto px-5 py-8">
          <div className="w-full max-w-md text-center">
            {/* Two empty states, and they must not look alike: "nothing has
                been said yet" is the conversation waiting for its first turn,
                while a lost first turn is a message the user believes they
                already sent. The second one names the reason and points at the
                composer below, which is the only way to recover the turn. */}
            {seedFailure !== null ? (
              <>
                <span className="mx-auto flex size-11 items-center justify-center rounded-lg bg-destructive/10 text-destructive">
                  <TriangleAlert className="size-5" aria-hidden="true" />
                </span>
                <p
                  role="alert"
                  className="mt-4 text-pretty text-body leading-6 text-destructive"
                >
                  {seedFailure
                    ? t(($) => $.alignment.seed_failed_notice, {
                        reason: seedFailure,
                      })
                    : t(($) => $.alignment.seed_failed_notice_plain)}
                </p>
              </>
            ) : (
              <>
                <span className="mx-auto flex size-11 items-center justify-center rounded-lg bg-primary/10 text-primary">
                  <MessageSquare className="size-5" aria-hidden="true" />
                </span>
                <p className="mt-4 text-pretty text-body leading-6 text-muted-foreground">
                  {t(($) => $.alignment.preview_empty)}
                </p>
              </>
            )}
          </div>
        </div>
      )}

      {question && !readOnly ? (
        <div className="border-t bg-muted/20 px-5 py-3">
          <div className="flex items-start gap-2">
            <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
              <Sparkles className="size-3.5" aria-hidden="true" />
            </span>
            <p className="min-w-0 flex-1 text-body font-medium">
              {question.question}
            </p>
          </div>
          {question.options.length > 0 ? (
            <div className="mt-2.5 flex flex-wrap gap-1.5 pl-7">
              {question.options.map((option) => (
                <Button
                  key={option.value}
                  type="button"
                  size="sm"
                  // The recommended answer is the one the carrier would pick
                  // itself, so it reads as the default rather than as one of
                  // several equals. It is a shortcut, not a decision: the
                  // composer below is always the custom answer.
                  variant={option.recommended ? "secondary" : "outline"}
                  disabled={sending || pending || !runtimeOnline}
                  onClick={() => void onSend(option.value)}
                >
                  {option.label}
                  {option.recommended ? (
                    <span className="text-caption text-muted-foreground">
                      {t(($) => $.alignment.question_recommended)}
                    </span>
                  ) : null}
                </Button>
              ))}
            </div>
          ) : null}
          <p className="mt-2 pl-7 text-caption text-muted-foreground">
            {t(($) => $.alignment.question_custom_hint)}
          </p>
        </div>
      ) : null}

      {error && !readOnly ? (
        <div
          role="alert"
          aria-live="polite"
          className="mx-5 mb-3 flex items-start gap-2 rounded-md bg-destructive/5 px-3 py-2 text-body text-destructive"
        >
          <span className="min-w-0 flex-1">{error}</span>
        </div>
      ) : null}

      {!readOnly ? (
        <ChatInput
          onSend={onSend}
          onStop={onStop}
          isRunning={pending || sending}
          disabled={!runtimeOnline}
          // An alignment session is a chat session with a runtime behind it, so
          // the upload affordance exists exactly when that runtime can answer.
          uploadEnabled={runtimeOnline}
          agentName={agentName}
          draftKeyOverride={draftKey}
          editorKeyOverride={draftKey}
        />
      ) : null}
    </section>
  );
}
