/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { ApiClient, setApiInstance } from "../api";
import type { ApiClient as ApiClientType } from "../api/client";
import type { IssueDraftSession } from "../types";
import { IssueDraftSessionUnrecognizedError, useStartIssueDraft } from "./mutations";

/**
 * DENE-370: the alignment entry point uploads through the shared create-dialog
 * pool, so the first turn has to hand its attachment ids to the same
 * `sendChatMessage` transport every chat surface uses (DENE-369). Without the
 * passthrough the ids are silently dropped and the conversation starts with a
 * request whose screenshots never arrived.
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

  afterEach(() => {
    vi.unstubAllGlobals();
  });

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
    } as unknown as ApiClientType);
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

  /**
   * DENE-422: a lost first turn has to survive as a REASON, not as a bare
   * `seeded: false`. The page the caller navigates to is the only place it can
   * be shown, and "it failed" without the failure is what left the entry silent.
   */
  it("keeps the first turn's rejection in the result instead of discarding it", async () => {
    const failure = new Error("runtime_unusable");
    sendChatMessage.mockRejectedValue(failure);
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    const started = await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "add dark mode",
    });

    // Still a result, not a throw: the draft exists server-side, so the caller
    // must navigate to it rather than strand it.
    expect(started.seeded).toBe(false);
    expect(started.draftId).toBe("sess-new");
    expect(started.seedError).toBe(failure);
  });

  /**
   * The malformed-response contract from CLAUDE.md's API-compatibility rules:
   * `parseWithFallback` degrades an unparseable body to
   * `EMPTY_ISSUE_DRAFT_SESSION`, and that must be reported as response drift —
   * never as "the session was not created", because the server DID create it and
   * a user told otherwise creates a second, orphaned draft.
   *
   * Driven through the real `ApiClient` with a stubbed fetch so the schema
   * fallback itself is what produces the empty id.
   */
  it("names an unparseable create-session response as drift", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ agent_id: "agent-1" }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);
    setApiInstance(new ApiClient("https://api.example.test") as ApiClientType);

    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await expect(
      result.current.mutateAsync({ runtimeId: "rt-1", request: "add dark mode" }),
    ).rejects.toBeInstanceOf(IssueDraftSessionUnrecognizedError);
    // Drift is not a send failure: nothing was sent, and no draft page exists.
    expect(sendChatMessage).not.toHaveBeenCalled();
  });
});
