// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { IssueDraft, IssueDraftSession, IssueDraftSummary } from "../types";
import {
  appendIssueDraftSummary,
  findIssueDraft,
  patchIssueDraftSummary,
} from "./queries";

function row(overrides: Partial<IssueDraftSummary> = {}): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 1,
    draft: { title: "Dark mode", description: "Add it.", status: "", priority: "" },
    issue_id: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "Align a new issue",
    runtime_id: "rt-1",
    last_message_content: "add dark mode",
    last_message_role: "user",
    last_message_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("findIssueDraft", () => {
  it("addresses a draft by its carrier session id", () => {
    const rows = [row({ chat_session_id: "a" }), row({ chat_session_id: "b" })];
    expect(findIssueDraft(rows, "b")?.chat_session_id).toBe("b");
    expect(findIssueDraft(rows, "missing")).toBeUndefined();
  });
});

describe("patchIssueDraftSummary", () => {
  it("applies a save response without dropping the summary-only fields", () => {
    const updated: IssueDraft = {
      chat_session_id: "sess-1",
      workspace_id: "ws-1",
      status: "ready",
      revision: 2,
      draft: { title: "Dark mode", description: "Add it.", status: "", priority: "" },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:01:00Z",
    };
    const [patched] = patchIssueDraftSummary([row()], updated);
    expect(patched?.revision).toBe(2);
    expect(patched?.status).toBe("ready");
    // A save response carries no preview or runtime; losing them would blank
    // out the very list the user picks the draft from.
    expect(patched?.last_message_content).toBe("add dark mode");
    expect(patched?.runtime_id).toBe("rt-1");
  });

  it("never invents a row for a draft the list does not have", () => {
    expect(patchIssueDraftSummary(undefined, row())).toEqual([]);
  });
});

describe("appendIssueDraftSummary", () => {
  const session: IssueDraftSession = {
    session_id: "sess-new",
    agent_id: "agent-1",
    runtime_id: "rt-9",
    draft: {
      chat_session_id: "sess-new",
      workspace_id: "ws-1",
      status: "draft",
      revision: 1,
      draft: { title: "", description: "add dark mode", status: "", priority: "" },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
  };

  it("lists a just-created conversation first so its page finds it", () => {
    const rows = appendIssueDraftSummary([row()], session, {
      content: "add dark mode",
      at: "2026-01-01T00:02:00Z",
    });
    expect(rows.map((entry) => entry.chat_session_id)).toEqual(["sess-new", "sess-1"]);
    expect(rows[0]?.runtime_id).toBe("rt-9");
    expect(rows[0]?.last_message_role).toBe("user");
  });

  it("records no preview when the first turn never landed", () => {
    // The draft still exists and holds the request in its description; claiming
    // a message that was never delivered would misreport the conversation.
    const [created] = appendIssueDraftSummary(undefined, session, null);
    expect(created?.last_message_content).toBe("");
    expect(created?.last_message_role).toBe("");
  });

  it("merges instead of duplicating a row the list already has", () => {
    const existing = row({ chat_session_id: "sess-new", revision: 1 });
    const rows = appendIssueDraftSummary([existing], session, null);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.chat_session_id).toBe("sess-new");
  });
});
