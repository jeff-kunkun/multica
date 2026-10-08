import { describe, expect, it } from "vitest";
import type { ChatTicket } from "@multica/core/types";
import { groupChatTickets } from "./chat-tickets";

const ticket = (id: string, created_at: string): ChatTicket => ({
  id,
  identifier: id.toUpperCase(),
  title: id,
  status: "todo",
  priority: "none",
  assignee_type: null,
  assignee_id: null,
  created_at,
  updated_at: created_at,
});

describe("groupChatTickets", () => {
  const messages = [
    { id: "u1", role: "user", created_at: "2026-10-01T10:00:00Z" },
    { id: "a1", role: "assistant", created_at: "2026-10-01T10:01:00Z" },
    { id: "u2", role: "user", created_at: "2026-10-01T10:02:00Z" },
    { id: "a2", role: "assistant", created_at: "2026-10-01T10:03:00Z" },
  ];

  it("hangs each ticket under the first reply at or after it", () => {
    const { byMessage, tail } = groupChatTickets(messages, [
      ticket("t1", "2026-10-01T10:00:30Z"),
      ticket("t2", "2026-10-01T10:01:00Z"),
      ticket("t3", "2026-10-01T10:02:30Z"),
    ]);
    expect(byMessage.get("a1")?.map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(byMessage.get("a2")?.map((t) => t.id)).toEqual(["t3"]);
    expect(byMessage.has("u1")).toBe(false);
    expect(tail).toEqual([]);
  });

  it("sends tickets from a still-running turn to the tail", () => {
    const { byMessage, tail } = groupChatTickets(messages, [
      ticket("t4", "2026-10-01T10:04:00Z"),
    ]);
    expect(byMessage.size).toBe(0);
    expect(tail.map((t) => t.id)).toEqual(["t4"]);
  });
});
