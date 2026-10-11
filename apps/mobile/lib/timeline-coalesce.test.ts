import { expect, it } from "vitest";
import type { TimelineEntry } from "@multica/core/types";
import { coalesceTimeline } from "./timeline-coalesce";

it("keeps member and distinct bot activity attribution separate", () => {
  const base: TimelineEntry = { id: "a", type: "activity", actor_type: "member", actor_id: "u", action: "status_changed", created_at: "2026-10-11T00:00:00Z" };
  const entries = [base, { ...base, id: "b", via_bot_id: "bot-1", via_bot_name: "One" }, { ...base, id: "c", via_bot_id: "bot-2", via_bot_name: "Two" }];
  expect(coalesceTimeline(entries)).toHaveLength(3);
  expect(coalesceTimeline([entries[1], { ...entries[1], id: "d" }])).toMatchObject([{ id: "d", coalesced_count: 2, via_bot_name: "One" }]);
});
