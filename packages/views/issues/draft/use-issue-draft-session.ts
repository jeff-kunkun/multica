"use client";

import { useCallback, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { chatKeys, chatMessagesOptions, pendingChatTaskOptions } from "@multica/core/chat/queries";
import { upsertChatMessageToCaches } from "@multica/core/chat/message-cache";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  issueDraftCanConfirm,
  issueDraftIsCreatable,
  issueDraftListOptions,
  issueDraftStage,
  encodeIssueDraftInput,
  findIssueDraft,
  mergeIssueDraftPayload,
  parseIssueDraftBlock,
  useAbandonIssueDraft,
  useFinalizeIssueDraft,
  useSaveIssueDraft,
  useSwitchIssueDraftRuntime,
  type IssueDraftStage,
} from "@multica/core/issue-drafts";
import { runtimeListOptions } from "@multica/core/runtimes";
import type {
  ChatMessage,
  IssueDraftPayload,
  IssueDraftSummary,
  RuntimeDevice,
} from "@multica/core/types";
import { useT } from "../../i18n";

const EMPTY_MESSAGES: ChatMessage[] = [];
const EMPTY_DRAFTS: readonly IssueDraftSummary[] = [];

export interface IssueDraftSession {
  stage: IssueDraftStage;
  /** The server-owned draft, absent until the list lands or once it is retired. */
  draft: IssueDraftPayload | null;
  revision: number | null;
  /** The conversation is gone server-side; the page must leave. */
  missing: boolean;
  /**
   * The conversation is alive but the draft is no longer one of this user's
   * unfinished ones — it became an issue elsewhere, or was discarded. Not an
   * error: the page says so instead of offering actions the server refuses.
   */
  retired: boolean;
  loading: boolean;
  loadFailed: boolean;
  messages: ChatMessage[];
  messagesLoading: boolean;
  /** A turn is running on the carrier. */
  pending: boolean;
  /** The in-flight turn, as the transcript renders it. */
  pendingTask: { task_id?: string; status?: string; created_at?: string } | undefined;
  sending: boolean;
  error: string | null;
  runtime: RuntimeDevice | null;
  runtimeOnline: boolean;
  switchingRuntime: boolean;
  canConfirm: boolean;
  saving: boolean;
  saved: boolean;
  confirming: boolean;
  abandoning: boolean;
  createdIssueId: string | null;
  send: (content: string, commitInput?: () => void) => Promise<boolean>;
  save: (draft: IssueDraftPayload, status?: "draft" | "ready") => Promise<boolean>;
  /**
   * Folds the carrier's latest `<issue_draft>` block into `current` and saves
   * the result as `ready`. This is "the conversation has converged" expressed
   * as one action: parsing and persisting together is what makes the preview
   * the thing the server will actually create from, rather than a rendering of
   * a message that could still change under it.
   */
  generatePreview: (current: IssueDraftPayload) => Promise<boolean>;
  confirm: () => Promise<boolean>;
  abandon: () => Promise<boolean>;
  switchRuntime: (runtimeId: string) => Promise<string | null>;
  stop: () => Promise<void>;
  retry: () => void;
  clearError: () => void;
}

/**
 * Lifecycle of one alignment conversation: exchange turns on the hidden
 * carrier, save the structured draft, confirm it into an issue, or discard it.
 *
 * The conversation is identified by the id in the URL and nothing here holds
 * session state of its own, which is what makes a refresh, a back/forward and a
 * reopened desktop tab land back in the same alignment. There is no polling:
 * the global realtime sync already invalidates `chatKeys.messages` /
 * `chatKeys.pendingTask` per session id on `chat:message`, `chat:done` and the
 * task lifecycle events, exactly as it does for the main chat window.
 */
export function useIssueDraftSession(draftId: string): IssueDraftSession {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const listQuery = useQuery(issueDraftListOptions(wsId));
  const messagesQuery = useQuery(chatMessagesOptions(draftId));
  const pendingQuery = useQuery(pendingChatTaskOptions(draftId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));

  const row = findIssueDraft(listQuery.data ?? EMPTY_DRAFTS, draftId);
  const messages = messagesQuery.data ?? EMPTY_MESSAGES;
  const pending = !!pendingQuery.data?.task_id;

  // A successful confirm is final even after the refetch drops the completed
  // draft out of the unfinished list, so it outranks everything the list says.
  const [createdIssueId, setCreatedIssueId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [saved, setSaved] = useState(false);

  const saveMutation = useSaveIssueDraft(wsId);
  const abandonMutation = useAbandonIssueDraft(wsId);
  const finalizeMutation = useFinalizeIssueDraft(wsId);
  const runtimeMutation = useSwitchIssueDraftRuntime(wsId);

  const draft = row?.draft ?? null;
  const revision = row?.revision ?? null;

  const stage = issueDraftStage({
    status: row?.status ?? "draft",
    creating: finalizeMutation.isPending,
    createdIssueId,
  });

  // Read off the transcript fetch, not "absent from the list": a conversation
  // with no draft row is a different situation from one that no longer exists,
  // and only a 404 proves the latter.
  const missing =
    messagesQuery.error instanceof ApiError && messagesQuery.error.status === 404;
  const retired = !missing && !createdIssueId && listQuery.isSuccess && !row;

  const runtime = useMemo(
    () => runtimesQuery.data?.find((device) => device.id === row?.runtime_id) ?? null,
    [row?.runtime_id, runtimesQuery.data],
  );
  const runtimeOnline = runtime?.status === "online";

  const canConfirm = issueDraftCanConfirm({
    stage,
    hasTitle: draft ? issueDraftIsCreatable(draft) : false,
    pending,
  });

  /**
   * Sends one turn.
   *
   * Every turn goes out as a `MULTICA_ISSUE_DRAFT_INPUT` envelope carrying the
   * draft the user is asking about, not just their words. The carrier's
   * instructions say to "preserve good existing draft fields supplied in the
   * user's message" — supply nothing and the next reply rebuilds the draft from
   * one message, dropping fields that were already agreed or edited by hand in
   * the preview. The transcript decodes the envelope back for display.
   *
   * `commitInput` is the composer's clear, and it runs the moment the server
   * has accepted the message and the caches render it — not after the
   * reconciling invalidations settle. Awaiting those would hold the user's text
   * in the box for three more round-trips while their message is already on
   * screen (MUL-5181).
   */
  const send = useCallback(
    async (content: string, commitInput?: () => void): Promise<boolean> => {
      const text = content.trim();
      if (!text || !draftId || pending || sending || !draft) return false;
      setError(null);
      setSending(true);
      const wire = encodeIssueDraftInput(text, draft);
      try {
        const result = await api.sendChatMessage(draftId, wire);
        const createdAt = new Date().toISOString();
        upsertChatMessageToCaches(
          qc,
          draftId,
          {
            id: result.message_id,
            chat_session_id: draftId,
            role: "user",
            content: wire,
            task_id: result.task_id,
            created_at: createdAt,
          },
          { seedIfMissing: true },
        );
        qc.setQueryData(chatKeys.pendingTask(draftId), {
          task_id: result.task_id,
          status: "queued",
          created_at: createdAt,
        });
        commitInput?.();
        void qc.invalidateQueries({ queryKey: chatKeys.messages(draftId) });
        void qc.invalidateQueries({ queryKey: chatKeys.messagesPage(draftId) });
        void qc.invalidateQueries({ queryKey: chatKeys.pendingTask(draftId) });
        return true;
      } catch (err) {
        setError(err instanceof Error ? err.message : t(($) => $.alignment.send_failed));
        return false;
      } finally {
        setSending(false);
      }
    },
    [draft, draftId, pending, qc, sending, t],
  );

  const save = useCallback(
    async (
      next: IssueDraftPayload,
      status?: "draft" | "ready",
    ): Promise<boolean> => {
      if (revision === null) return false;
      setError(null);
      try {
        await saveMutation.mutateAsync({
          draftId,
          draft: next,
          ...(status ? { status } : {}),
          expectedRevision: revision,
        });
        setSaved(true);
        return true;
      } catch {
        // The mutation's own onError already re-read the list; the panel is
        // about to show the revision that actually won.
        setError(t(($) => $.alignment.save_failed));
        return false;
      }
    },
    [draftId, revision, saveMutation, t],
  );

  /**
   * `current` is the panel's editor value, not the stored draft: the user's
   * unsaved edits are part of what they are asking to have previewed, and
   * rebuilding from the stored draft would silently discard them.
   */
  const generatePreview = useCallback(
    async (current: IssueDraftPayload): Promise<boolean> => {
      const latest = [...messages]
        .reverse()
        .find(
          (message) =>
            message.role === "assistant" &&
            parseIssueDraftBlock(message.content) !== null,
        );
      const merged = mergeIssueDraftPayload(
        current,
        latest ? parseIssueDraftBlock(latest.content) : null,
      );
      if (!issueDraftIsCreatable(merged)) return false;
      return save(merged, "ready");
    },
    [messages, save],
  );

  const confirm = useCallback(async (): Promise<boolean> => {
    if (revision === null) return false;
    setError(null);
    try {
      const result = await finalizeMutation.mutateAsync({
        draftId,
        expectedRevision: revision,
      });
      setCreatedIssueId(result.issue_id);
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.alignment.confirm_failed));
      return false;
    }
  }, [draftId, finalizeMutation, revision, t]);

  const abandon = useCallback(async (): Promise<boolean> => {
    setError(null);
    try {
      await abandonMutation.mutateAsync(draftId);
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.alignment.abandon_failed));
      return false;
    }
  }, [abandonMutation, draftId, t]);

  const switchRuntime = useCallback(
    async (runtimeId: string): Promise<string | null> => {
      setError(null);
      try {
        const result = await runtimeMutation.mutateAsync({ draftId, runtimeId });
        // Follow the runtime the server says it bound. Resolving here at all
        // means the rebind committed, so refusing to move the picker would
        // leave it pointing at a runtime that no longer executes anything.
        return result.runtime_id || runtimeId;
      } catch (err) {
        setError(err instanceof Error ? err.message : t(($) => $.alignment.runtime_failed));
        return null;
      }
    },
    [draftId, runtimeMutation, t],
  );

  const stop = useCallback(async () => {
    const taskId = pendingQuery.data?.task_id;
    if (!taskId || !draftId) return;
    qc.setQueryData(chatKeys.pendingTask(draftId), {});
    try {
      await api.cancelTaskById(taskId);
    } finally {
      // The cancel may or may not have landed; re-read either way rather than
      // trusting the optimistic clear.
      void qc.invalidateQueries({ queryKey: chatKeys.messages(draftId) });
      void qc.invalidateQueries({ queryKey: chatKeys.pendingTask(draftId) });
    }
  }, [draftId, pendingQuery.data?.task_id, qc]);

  const loading = listQuery.isLoading || messagesQuery.isLoading;

  return {
    stage,
    draft,
    revision,
    missing,
    retired,
    loading,
    loadFailed: listQuery.isError,
    messages,
    messagesLoading: messagesQuery.isLoading,
    pending,
    pendingTask: pendingQuery.data,
    sending,
    error,
    runtime,
    runtimeOnline,
    switchingRuntime: runtimeMutation.isPending,
    canConfirm,
    saving: saveMutation.isPending,
    saved,
    confirming: finalizeMutation.isPending,
    abandoning: abandonMutation.isPending,
    createdIssueId,
    send,
    save,
    generatePreview,
    confirm,
    abandon,
    switchRuntime,
    stop,
    retry: () => {
      void listQuery.refetch();
      void messagesQuery.refetch();
    },
    clearError: () => setError(null),
  };
}
