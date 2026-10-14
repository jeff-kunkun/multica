// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { ChatSession } from "../types";
import { filterChatSessionsByProject, rankChatProjects } from "./project-bar";

function session(id: string, over: Partial<ChatSession>): ChatSession {
  return {
    id,
    workspace_id: "ws",
    agent_id: "a",
    creator_id: "u",
    project_id: null,
    title: id,
    status: "active",
    has_unread: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...over,
  };
}

describe("rankChatProjects", () => {
  const sessions = [
    session("s1", { project_id: "p1", updated_at: "2026-10-02T00:00:00Z" }),
    session("s2", { project_id: "p2", updated_at: "2026-10-05T00:00:00Z", has_unread: true }),
    session("s3", { project_id: "p1", updated_at: "2026-10-03T00:00:00Z" }),
    session("s4", { project_id: "p3", status: "archived", has_unread: true }),
    session("s5", { project_id: null }),
    session("s6", { project_id: "gone" }),
  ];

  it("lists projects with active chats, most recent first, with counts and unread", () => {
    expect(rankChatProjects(["p1", "p2", "p3"], [], sessions)).toEqual([
      { id: "p2", chatCount: 1, hasUnread: true, pinned: false },
      { id: "p1", chatCount: 2, hasUnread: false, pinned: false },
    ]);
  });

  it("puts pinned projects with chats first, each group by most recent chat", () => {
    expect(rankChatProjects(["p1", "p2", "p3", "p4"], ["p1", "p3", "p4", "gone"], [
      ...sessions,
      session("s7", { project_id: "p4", updated_at: "2026-10-04T00:00:00Z" }),
    ])).toEqual([
      { id: "p4", chatCount: 1, hasUnread: false, pinned: true },
      { id: "p1", chatCount: 2, hasUnread: false, pinned: true },
      { id: "p2", chatCount: 1, hasUnread: true, pinned: false },
    ]);
  });

  it("ranks by the last message time when present", () => {
    const withMessage = [
      session("a", { project_id: "p1", updated_at: "2026-10-09T00:00:00Z" }),
      session("b", {
        project_id: "p2",
        updated_at: "2026-10-01T00:00:00Z",
        last_message: { content: "x", role: "user", created_at: "2026-10-10T00:00:00Z" },
      }),
    ];
    expect(rankChatProjects(["p1", "p2"], [], withMessage).map((c) => c.id)).toEqual(["p2", "p1"]);
  });
});

describe("filterChatSessionsByProject", () => {
  const sessions = [session("s1", { project_id: "p1" }), session("s2", { project_id: null })];
  it("keeps everything without a filter and narrows with one", () => {
    expect(filterChatSessionsByProject(sessions, null)).toBe(sessions);
    expect(filterChatSessionsByProject(sessions, "p1").map((s) => s.id)).toEqual(["s1"]);
  });
});
