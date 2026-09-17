/**
 * @vitest-environment jsdom
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { IssueDraftSession } from "../types";
import { useStartIssueDraft } from "./mutations";

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
});
