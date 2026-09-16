import {
  useMutation,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";
import { chatKeys } from "../chat/queries";
import { upsertChatMessageToCaches } from "../chat/message-cache";
import type {
  IssueDraft,
  IssueDraftPayload,
  IssueDraftSession,
  IssueDraftSummary,
} from "../types";
import { encodeIssueDraftInput } from "./protocol";
import {
  appendIssueDraftSummary,
  issueDraftKeys,
  patchIssueDraftSummary,
} from "./queries";

/**
 * Every write an alignment conversation can make, as React Query mutations.
 *
 * Mutations, not hand-rolled `useState` flags: the confirm button has to be
 * disabled for exactly as long as the create is in flight — a double-clicked
 * confirm is a second request the protocol tolerates, but a second *button*
 * press is what the user sees as "nothing happened". `isPending` is the only
 * flag that cannot drift from the request it describes.
 */

export interface StartIssueDraftResult {
  session: IssueDraftSession;
  draftId: string;
  /**
   * False when the conversation was opened but its first turn could not be
   * delivered. The draft still exists, and the idea is still stored in it, so
   * the caller navigates either way rather than stranding a draft the user
   * cannot see and would create again.
   */
  seeded: boolean;
}

/**
 * Opens an alignment conversation and asks it the user's question.
 *
 * Two calls, one action. The session is created first and its id is what the
 * page is addressed by; the first turn is sent immediately after, because a
 * conversation the user has to restate their request into is not an
 * improvement over a form. The idea is also stored in the draft itself before
 * either call returns, so a failed send costs the turn, never the request.
 */
export function useStartIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      runtimeId: string;
      model?: string;
      /** What the user already typed at the entry point. */
      request: string;
    }): Promise<StartIssueDraftResult> => {
      const request = input.request.trim();
      const session = await api.createIssueDraftSession({
        runtime_id: input.runtimeId,
        model: input.model?.trim() || undefined,
        draft: seedDraft(request),
      });
      const draftId = session.session_id;
      if (!draftId) throw new Error("issue draft session was not created");
      try {
        const sent = await api.sendChatMessage(
          draftId,
          encodeIssueDraftInput(request, session.draft.draft),
        );
        // The same door every chat surface uses (MUL-5711): the send seeds the
        // caches, so the conversation shows the user's own turn immediately
        // instead of waiting for a refetch that the realtime echo may win.
        upsertChatMessageToCaches(
          qc,
          draftId,
          {
            id: sent.message_id,
            chat_session_id: draftId,
            role: "user",
            content: encodeIssueDraftInput(request, session.draft.draft),
            task_id: sent.task_id,
            created_at: new Date().toISOString(),
          },
          { seedIfMissing: true },
        );
        qc.setQueryData(chatKeys.pendingTask(draftId), {
          task_id: sent.task_id,
          status: "queued",
          created_at: new Date().toISOString(),
        });
        return { session, draftId, seeded: true };
      } catch {
        return { session, draftId, seeded: false };
      }
    },
    onSuccess: (result) => {
      // Seed before invalidating: the page this create navigates to reads the
      // list on its first render, and a row that is missing from it reads as a
      // finished alignment.
      const at = new Date().toISOString();
      qc.setQueryData<IssueDraftSummary[]>(
        issueDraftKeys.list(wsId),
        (rows) =>
          appendIssueDraftSummary(
            rows,
            result.session,
            result.seeded ? { content: result.session.draft.draft.description, at } : null,
          ),
      );
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
      void qc.invalidateQueries({
        queryKey: chatKeys.messages(result.draftId),
      });
      void qc.invalidateQueries({
        queryKey: chatKeys.pendingTask(result.draftId),
      });
    },
  });
}

/**
 * Persists what the conversation has agreed so far.
 *
 * `expected_revision` is the revision the caller was looking at. A save the
 * server refuses because that view is superseded — by another tab, or by the
 * page's own refetch — is not retried here: the answer is to reload and show
 * the user what actually changed, which is why the failure invalidates the list
 * rather than replaying the write on top of a draft nobody has seen.
 */
export function useSaveIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      draftId: string;
      draft: IssueDraftPayload;
      status?: "draft" | "ready";
      expectedRevision: number;
    }) =>
      api.updateIssueDraft(input.draftId, {
        draft: input.draft,
        ...(input.status ? { status: input.status } : {}),
        expected_revision: input.expectedRevision,
      }),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/** Discards a draft. The conversation itself is left alone. */
export function useAbandonIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (draftId: string) => api.abandonIssueDraft(draftId),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      // A retired draft is no longer in the unfinished list, so the row this
      // patch just updated is about to disappear; the refetch is what removes
      // it rather than a local removal, which would hide a server refusal.
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Confirms a draft into an issue.
 *
 * Deliberately not retried by the network layer: the protocol is idempotent, so
 * a repeat is safe, but a silent automatic repeat would hide the very
 * conflict the user needs to see. `.mutateAsync` is what lets the caller keep
 * the panel in `creating` until the server answers.
 */
export function useFinalizeIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { draftId: string; expectedRevision: number }) =>
      api.finalizeIssueDraft(input.draftId, {
        expected_revision: input.expectedRevision,
      }),
    onSuccess: (result) => {
      applyDraftRow(qc, wsId, result.draft);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
      // The issue now exists, so every surface that lists issues is stale.
      void qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
    onError: () => {
      // A refused confirm is usually a superseded revision; re-read so the
      // panel stops offering a create the server has already answered.
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Rebinds a live conversation to another runtime. Callers must not show the new
 * runtime as selected until this resolves — the picker showing runtime B while
 * messages still run on A is exactly the bug the builder's equivalent fixed
 * (MUL-5163).
 */
export function useSwitchIssueDraftRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { draftId: string; runtimeId: string }) =>
      api.switchIssueDraftRuntime(input.draftId, {
        runtime_id: input.runtimeId,
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Switches how the carrier asks: guided questions, or plain dialogue.
 *
 * The response is applied to the list cache rather than merely invalidated for
 * the same reason a save is: the switch is a determinate field change the user
 * is standing in front of, and the recorded prompt version it returns is the
 * audit value the page displays. A failure re-reads instead — a switch refused
 * because a reply is in flight must leave the control showing what is actually
 * running.
 */
export function useSwitchIssueDraftPolicy(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { draftId: string; policy: string }) =>
      api.switchIssueDraftPolicy(input.draftId, { policy: input.policy }),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/** The idea, kept server-side from the first moment so a lost turn loses nothing. */
function seedDraft(request: string): Partial<IssueDraftPayload> {
  return {
    title: "",
    description: request,
    status: "",
    priority: "",
  };
}

function applyDraftRow(
  qc: QueryClient,
  wsId: string,
  updated: IssueDraft,
): void {
  qc.setQueryData<IssueDraftSummary[]>(
    issueDraftKeys.list(wsId),
    (rows) => patchIssueDraftSummary(rows, updated),
  );
}
