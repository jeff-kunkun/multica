"use client";

import { MessageSquare } from "lucide-react";
import type { ChatMessage } from "@multica/core/types";
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
   *  server accepts the message. */
  onSend: (content: string, commitInput?: () => void) => Promise<boolean>;
  onStop: () => void;
  error: string | null;
}) {
  const { t } = useT("issues");
  const pending = !!pendingTask?.task_id;
  const draftKey = `issue-draft:${draftId}`;
  const agentName = t(($) => $.alignment.conversation_title);

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
      </header>

      {loading ? (
        <ChatMessageSkeleton />
      ) : messages.length > 0 || pending ? (
        <ChatMessageList
          messages={messages}
          pendingTask={pendingTask}
          availability={runtimeOnline ? "online" : "offline"}
        />
      ) : (
        <div className="flex min-h-0 flex-1 items-center justify-center overflow-y-auto px-5 py-8">
          <div className="w-full max-w-md text-center">
            <span className="mx-auto flex size-11 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <MessageSquare className="size-5" aria-hidden="true" />
            </span>
            <p className="mt-4 text-pretty text-body leading-6 text-muted-foreground">
              {t(($) => $.alignment.preview_empty)}
            </p>
          </div>
        </div>
      )}

      {error ? (
        <div
          role="alert"
          aria-live="polite"
          className="mx-5 mb-3 flex items-start gap-2 rounded-md bg-destructive/5 px-3 py-2 text-body text-destructive"
        >
          <span className="min-w-0 flex-1">{error}</span>
        </div>
      ) : null}

      <ChatInput
        onSend={(content, _attachmentIds, commitInput) =>
          onSend(content, commitInput)
        }
        onStop={onStop}
        isRunning={pending || sending}
        disabled={!runtimeOnline}
        agentName={agentName}
        draftKeyOverride={draftKey}
        editorKeyOverride={draftKey}
      />
    </section>
  );
}
