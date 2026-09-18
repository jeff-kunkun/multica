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
import { issueDraftParentIssueId } from "./group";

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
    capabilities: { keys: [], version: "" },
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

  /**
   * DENE-423: a project chosen at the entry point is stored on the draft at
   * creation, because the server files the whole group under whatever the root
   * node carries. It is never shown to the carrier — the turn still carries the
   * same four fields — so the project has to survive in the stored draft alone.
   */
  it("stores the entry point's project on the draft it creates", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "add dark mode",
      projectId: "proj-1",
    });

    expect(createIssueDraftSession).toHaveBeenCalledTimes(1);
    const input = createIssueDraftSession.mock.calls[0]![0] as {
      draft: { project_id?: string; description: string };
    };
    expect(input.draft.project_id).toBe("proj-1");
    expect(input.draft.description).toBe("add dark mode");

    // The carrier has no project list and is not asked to guess one: the first
    // turn must not carry the field it was never told about.
    await waitFor(() => expect(sendChatMessage).toHaveBeenCalledTimes(1));
    expect(sendChatMessage.mock.calls[0]![1] as string).not.toContain("proj-1");
  });

  it("leaves the project absent when none was chosen", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({ runtimeId: "rt-1", request: "add dark mode" });

    const input = createIssueDraftSession.mock.calls[0]![0] as {
      draft: Record<string, unknown>;
    };
    // Absent rather than empty: "no project" is the field being missing, and an
    // empty string reads as a project named "".
    expect(input.draft).not.toHaveProperty("project_id");
  });

  /**
   * DENE-452: an alignment started from an existing issue is filed BENEATH it.
   * The confirm builds the group out of the stored payload, so the parent has to
   * be written at creation; a field that only lived in the dialog would never
   * reach the server, and the conversation would found a second top-level issue
   * instead of the children of the one the person was looking at.
   */
  it("stores the issue an alignment was started from as the draft's parent", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({
      runtimeId: "rt-1",
      request: "this needs breaking down",
      parentIssueId: "issue-7",
    });

    expect(createIssueDraftSession).toHaveBeenCalledTimes(1);
    const input = createIssueDraftSession.mock.calls[0]![0] as {
      draft: { parent_issue_id?: string; description: string };
    };
    expect(input.draft.parent_issue_id).toBe("issue-7");

    // The carrier is told where the work came from by the request itself, not by
    // this field: the first turn carries the same four draft fields as ever.
    await waitFor(() => expect(sendChatMessage).toHaveBeenCalledTimes(1));
    expect(sendChatMessage.mock.calls[0]![1] as string).not.toContain("issue-7");
  });

  it("leaves the parent absent for a standalone alignment", async () => {
    const { result } = renderHook(() => useStartIssueDraft(WORKSPACE_ID), {
      wrapper: createWrapper(queryClient),
    });

    await result.current.mutateAsync({ runtimeId: "rt-1", request: "add dark mode" });

    const input = createIssueDraftSession.mock.calls[0]![0] as {
      draft: Record<string, unknown>;
    };
    // Absent, exactly as it was before mid-flight alignment existed: an
    // alignment started from nowhere in particular founds its own top-level
    // issue, and the stored payload has to keep saying so.
    expect(input.draft).not.toHaveProperty("parent_issue_id");
    // Read through the helper the page navigates with: "absent" and "no parent"
    // are the same answer there, which is what keeps a standalone alignment
    // landing on the root it just created.
    expect(issueDraftParentIssueId(input.draft as { parent_issue_id?: null })).toBeNull();
  });
});
