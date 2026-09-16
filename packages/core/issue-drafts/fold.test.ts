// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { ChatMessage, IssueDraftPayload } from "../types";
import { planIssueDraftFold } from "./fold";

/**
 * Folding the carrier's proposal into the server draft is how "the question was
 * answered, so the draft changed" reaches the object finalize actually reads.
 * Every rule below is a way to lose the user's work, so they are pinned here
 * rather than through a rendered page: an unsaved edit, a reply folded twice, a
 * turn still running.
 */

function message(overrides: Partial<ChatMessage> = {}): ChatMessage {
  return {
    id: "m1",
    chat_session_id: "sess-1",
    role: "assistant",
    content: 'Understood.\n<issue_draft>{"title":"Dark mode"}</issue_draft>',
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  } as ChatMessage;
}

const STORED: IssueDraftPayload = {
  title: "",
  description: "Add dark mode.",
  status: "",
  priority: "",
};

function plan(overrides: Partial<Parameters<typeof planIssueDraftFold>[0]> = {}) {
  return planIssueDraftFold({
    messages: [message()],
    draft: STORED,
    status: "draft",
    revision: 4,
    localDirty: false,
    appliedReplyId: null,
    busy: false,
    ...overrides,
  });
}

describe("planIssueDraftFold", () => {
  it("merges the reply's block into the stored draft", () => {
    expect(plan()).toEqual({
      replyId: "m1",
      draft: { ...STORED, title: "Dark mode" },
      status: "draft",
    });
  });

  it("keeps a ready draft ready instead of dropping it back to draft", () => {
    expect(plan({ status: "ready" })?.status).toBe("ready");
  });

  it("does not fold over an unsaved edit in the preview panel", () => {
    expect(plan({ localDirty: true })).toBeNull();
  });

  it("folds each reply at most once", () => {
    expect(plan({ appliedReplyId: "m1" })).toBeNull();
  });

  it("writes nothing while a turn or a save of our own is in flight", () => {
    expect(plan({ busy: true })).toBeNull();
  });

  it("writes nothing when the reply changes none of the four fields", () => {
    expect(
      plan({
        messages: [
          message({ content: '<issue_draft>{"title":"","description":""}</issue_draft>' }),
        ],
      }),
    ).toBeNull();
  });

  it("ignores replies with no block, and the user's own turns", () => {
    expect(plan({ messages: [message({ content: "Which surfaces?" })] })).toBeNull();
    expect(
      plan({
        messages: [
          message({ id: "u1", role: "user", content: "add dark mode" }),
        ],
      }),
    ).toBeNull();
  });

  it("folds the newest carrier reply, not the first one", () => {
    const fold = plan({
      messages: [
        message({ id: "m1", content: '<issue_draft>{"title":"First"}</issue_draft>' }),
        message({ id: "u1", role: "user", content: "keep going" }),
        message({ id: "m2", content: '<issue_draft>{"title":"Second"}</issue_draft>' }),
      ],
    });
    expect(fold?.replyId).toBe("m2");
    expect(fold?.draft.title).toBe("Second");
  });

  it("does nothing without a stored draft or a revision to write against", () => {
    expect(plan({ draft: null })).toBeNull();
    expect(plan({ revision: null })).toBeNull();
  });

  it("leaves terminal drafts alone", () => {
    expect(plan({ status: "completed" })).toBeNull();
    expect(plan({ status: "abandoned" })).toBeNull();
  });
});
