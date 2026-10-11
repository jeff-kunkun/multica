import { describe, expect, it } from "vitest";
import { ChatMessageSchema, CommentSchema, TimelineEntriesSchema } from "./schemas";

const comment = { id: "c", issue_id: "i", author_type: "member", author_id: "u", content: "hello", type: "comment", parent_id: null, created_at: "", updated_at: "" };
const message = { id: "m", chat_session_id: "s", role: "user" };
const activity = { id: "a", type: "activity", actor_type: "member", actor_id: "u", created_at: "" };
const parsers = [
  (fields: object) => CommentSchema.parse({ ...comment, ...fields }),
  (fields: object) => ChatMessageSchema.parse({ ...message, ...fields }),
  (fields: object) => TimelineEntriesSchema.parse([{ ...activity, ...fields }])[0],
];

describe("historical bot attribution", () => {
  for (const [index, parse] of parsers.entries()) {
    it(`preserves authenticated attribution on response ${index}`, () => {
      expect(parse({ via_bot_id: "bot-id", via_bot_name: "Archived Bot" })).toMatchObject({ via_bot_id: "bot-id", via_bot_name: "Archived Bot" });
    });
    it(`keeps old-server and malformed optional responses readable ${index}`, () => {
      expect(parse({}).via_bot_name).toBeUndefined();
      expect(parse({ via_bot_name: 42, via_bot_id: [] }).via_bot_name).toBeUndefined();
      expect(parse({ via_bot_name: null }).via_bot_name).toBeNull();
    });
  }
});
