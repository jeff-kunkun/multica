/**
 * @vitest-environment jsdom
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { setApiInstance } from "../api";
import { ApiError, clientErrorMessage } from "../api/client";
import type { ApiClient } from "../api/client";
import type { IssueDraftSession } from "../types";
import { useStartIssueDraft } from "./mutations";
import { issueDraftKeys } from "./queries";

/**
 * DENE-370: the alignment entry point uploads through the shared create-dialog
 * pool, so the first turn has to hand its attachment ids to the same
 * `sendChatMessage` transport every chat surface uses (DENE-369). Without the
 * passthrough the ids are silently dropped and the conversation starts with a
 * request whose screenshots never arrived.
 *
 * DENE-425 adds the other half of that contract — what the caller can learn when
 * something goes wrong. The create's 4xx has to survive this layer intact (the
 * entry face renders the server's own words), a first turn that never left has
 * to come back as a reason instead of a silent `seeded: false`, and a response
 * that dropped the session id must not read as a failed create: that is what
 * sent the user off to build a second, duplicate draft.
 */

const WORKSPACE_ID = "ws-1";

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
  };
}

function session(overrides: Partial<IssueDraftSession> = {}): IssueDraftSession {
  return {
    session_id: "sess-new",
    agent_id: "agent-1",
    runtime_id: "rt-1",
    draft: {
      chat_session_id: "sess-new",
      workspace_id: WORKSPACE_ID,
      status: "draft",
      revision: 1,
      draft: { title: "", description: "", status: "", priority: "" },
      policy: { key: "question", version: "1", guided: true },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
    ...overrides,
  };
}

describe("useStartIssueDraft", () => {
  let queryClient: QueryClient;
  let createIssueDraftSession: ReturnType<typeof vi.fn>;
  let sendChatMessage: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    createIssueDraftSession = vi.fn(async () => session());
    sendChatMessage = vi.fn(async () => ({
      message_id: "msg-1",
      task_id: "task-1",
    }));
    setApiInstance({
      createIssueDraftSession,
      sendChatMessage,
    } as unknown as ApiClient);
  });

  it("sends the first turn with the attachments the request references", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "add dark mode ![shot](https://cdn.example.test/shot.png)",
      attachmentIds: ["att-1", "att-2"],
    });

    expect(createIssueDraftSession).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(sendChatMessage).toHaveBeenCalledTimes(1));
    const [draftId, , attachmentIds] = sendChatMessage.mock.calls[0]!;
    expect(draftId).toBe("sess-new");
    expect(attachmentIds).toEqual(["att-1", "att-2"]);
  });

  it("omits the attachment argument for a text-only request", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({ runtimeId: "rt-1", request: "add dark mode" });

    await waitFor(() => expect(sendChatMessage).toHaveBeenCalledTimes(1));
    expect(sendChatMessage.mock.calls[0]![2]).toBeUndefined();
  });

  it("hands the chosen project to the server with the seed", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "add dark mode",
      projectId: "proj-1",
    });

    const input = createIssueDraftSession.mock.calls[0]![0] as {
      draft?: { project_id?: string };
    };
    expect(input.draft?.project_id).toBe("proj-1");
  });

  it("seeds no project when none was chosen", async () => {
    // "No project" is the absence of the field, not a null one — the same shape
    // every other create surface sends.
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({ runtimeId: "rt-1", request: "add dark mode" });

    const input = createIssueDraftSession.mock.calls[0]![0] as {
      draft?: Record<string, unknown>;
    };
    expect(input.draft).not.toHaveProperty("project_id");
  });

  it("rejects with the server's own 4xx, unrewritten", async () => {
    // The entry face renders `clientErrorMessage(error)` verbatim, so this
    // layer must not wrap, translate or replace the sentence the server wrote:
    // "runtime must be online to start an issue draft session" is the whole
    // diagnosis, and eleven other exits of the same endpoint are equally
    // specific (DENE-421).
    const failure = new ApiError(
      "runtime must be online to start an issue draft session",
      409,
      "Conflict",
    );
    createIssueDraftSession.mockRejectedValue(failure);
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    const error = await result.current
      .mutateAsync({ runtimeId: "rt-1", request: "add dark mode" })
      .then(() => null)
      .catch((err: unknown) => err);

    expect(error).toBe(failure);
    expect(clientErrorMessage(error)).toBe(
      "runtime must be online to start an issue draft session",
    );
  });

  it("carries out why a first turn never left", async () => {
    // A bare `catch {}` here is what left the user on an empty conversation
    // page with nothing saying the turn was lost. The draft and the request are
    // stored, so this is not a failure to retry — but it is one to explain, and
    // the server's 409 wording says which machine to fix.
    const failure = new ApiError("runtime is unusable for this user", 409, "Conflict");
    sendChatMessage.mockRejectedValue(failure);
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    const outcome = await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "add dark mode",
    });

    expect(outcome.seeded).toBe(false);
    expect(outcome.draftId).toBe("sess-new");
    expect(outcome.seedError).toBe(failure);
    expect(clientErrorMessage(outcome.seedError)).toBe(
      "runtime is unusable for this user",
    );
  });

  it("does not report a dropped session id as a create to retry", async () => {
    // `IssueDraftSessionSchema` requires `session_id`, so an id-less response
    // falls back to EMPTY_ISSUE_DRAFT_SESSION while the create itself committed
    // server-side. Throwing here told the user the alignment had failed, and
    // the only retry the form offers is a second conversation — a duplicate
    // nothing deduplicates. The answer is the partial success: no address, no
    // first turn, and no phantom row in the list cache.
    createIssueDraftSession.mockResolvedValue(session({ session_id: "" }));
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    const outcome = await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "add dark mode",
    });

    expect(outcome).toMatchObject({ draftId: "", seeded: false });
    expect(sendChatMessage).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(issueDraftKeys.list(WORKSPACE_ID))).toBeUndefined();
  });
});
