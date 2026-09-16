// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ChatMessage, IssueDraftPayload } from "../types";
import {
  decodeIssueDraftInput,
  encodeIssueDraftInput,
  issueDraftIsCreatable,
  issueDraftPendingQuestion,
  mergeIssueDraftPayload,
  parseIssueDraftBlock,
  parseIssueDraftQuestion,
  stripIssueDraftDirectives,
} from "./protocol";

const EMPTY: IssueDraftPayload = {
  title: "",
  description: "",
  status: "",
  priority: "",
};

describe("parseIssueDraftBlock", () => {
  it("reads title and description out of the carrier's block", () => {
    const reply =
      'Here is what I have so far.\n<issue_draft>{"title":"Ship the thing","description":"Do it carefully."}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({
      title: "Ship the thing",
      description: "Do it carefully.",
    });
  });

  it("takes the last block when a reply drafts twice", () => {
    const reply =
      '<issue_draft>{"title":"first"}</issue_draft> Actually, narrower:\n<issue_draft>{"title":"second"}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({ title: "second" });
  });

  it("drops empty strings rather than treating them as a clear", () => {
    // The carrier is told to leave status/priority empty unless the user
    // states them, so an empty value is "no opinion" — clearing the user's
    // own selection here would silently undo their edit on every reply.
    expect(parseIssueDraftBlock('<issue_draft>{"title":"T","status":"","priority":""}</issue_draft>')).toEqual(
      { title: "T" },
    );
  });

  it("returns null for a missing, unterminated or unparseable block", () => {
    expect(parseIssueDraftBlock("no block here")).toBeNull();
    expect(parseIssueDraftBlock('<issue_draft>{"title":"streaming')).toBeNull();
    expect(parseIssueDraftBlock("<issue_draft>not json</issue_draft>")).toBeNull();
    expect(parseIssueDraftBlock("<issue_draft>[1,2]</issue_draft>")).toBeNull();
  });

  it("recovers JSON whose string values contain literal newlines", () => {
    const reply = '<issue_draft>{"title":"T","description":"line one\nline two"}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({
      title: "T",
      description: "line one\nline two",
    });
  });
});

describe("stripIssueDraftDirectives", () => {
  it("removes complete and still-streaming blocks", () => {
    expect(stripIssueDraftDirectives('Done.\n<issue_draft>{"title":"T"}</issue_draft>')).toBe(
      "Done.",
    );
    expect(stripIssueDraftDirectives('Working…\n<issue_draft>{"title":"T"')).toBe("Working…");
  });

  it("removes the question block too", () => {
    // Both blocks are machine-readable. Leaving the question block in would
    // print raw JSON above the answer chips.
    expect(
      stripIssueDraftDirectives(
        'Who runs it?\n<issue_draft_question>{"question":"Who runs it?"}</issue_draft_question>',
      ),
    ).toBe("Who runs it?");
    expect(
      stripIssueDraftDirectives(
        'Who runs it?\n<issue_draft_question>{"question":"Who runs',
      ),
    ).toBe("Who runs it?");
  });

  it("leaves a reply with no block untouched", () => {
    expect(stripIssueDraftDirectives("Just a question?")).toBe("Just a question?");
  });
});

/**
 * The guided policy asks one question at a time and offers answers. The block is
 * a contract with the server prompt, and it fails the same way the draft block
 * does: a model that emits slightly malformed JSON must cost the chips, never
 * the conversation.
 */
describe("parseIssueDraftQuestion", () => {
  const block = (json: string) =>
    `Who should run it?\n<issue_draft_question>${json}</issue_draft_question>`;

  it("reads the question, its options and the recommended one", () => {
    expect(
      parseIssueDraftQuestion(
        block(
          '{"question":"Who runs it?","options":[{"label":"Bot","value":"Assign a bot","recommended":true},{"label":"Me","value":"Leave it unassigned"}]}',
        ),
      ),
    ).toEqual({
      question: "Who runs it?",
      options: [
        { label: "Bot", value: "Assign a bot", recommended: true },
        { label: "Me", value: "Leave it unassigned", recommended: false },
      ],
    });
  });

  it("keeps a question that has no usable options", () => {
    // The composer is the custom answer, so a question with nothing to click is
    // still a question the user can answer.
    expect(parseIssueDraftQuestion(block('{"question":"What is the deadline?"}'))).toEqual({
      question: "What is the deadline?",
      options: [],
    });
  });

  it("drops unusable options instead of the whole question", () => {
    expect(
      parseIssueDraftQuestion(
        block(
          '{"question":"Which?","options":[{"label":"","value":"x"},{"label":"A","value":"  "},{"label":"B","value":"b"},7]}',
        ),
      ),
    ).toEqual({
      question: "Which?",
      options: [{ label: "B", value: "b", recommended: false }],
    });
  });

  it("takes the last complete block, like the draft block does", () => {
    expect(
      parseIssueDraftQuestion(
        '<issue_draft_question>{"question":"First"}</issue_draft_question>\n<issue_draft_question>{"question":"Second"}</issue_draft_question>',
      )?.question,
    ).toBe("Second");
  });

  it("returns null for anything unusable", () => {
    expect(parseIssueDraftQuestion("no block")).toBeNull();
    expect(parseIssueDraftQuestion('<issue_draft_question>{"question":"streaming')).toBeNull();
    expect(parseIssueDraftQuestion(block("not json"))).toBeNull();
    expect(parseIssueDraftQuestion(block('{"question":"   "}'))).toBeNull();
    expect(parseIssueDraftQuestion(block('{"options":[]}'))).toBeNull();
  });
});

describe("issueDraftPendingQuestion", () => {
  const assistant = (id: string, content: string): ChatMessage =>
    ({ id, chat_session_id: "s", role: "assistant", content, created_at: "" }) as ChatMessage;
  const user = (id: string, content: string): ChatMessage =>
    ({ id, chat_session_id: "s", role: "user", content, created_at: "" }) as ChatMessage;

  it("is the question on the last message", () => {
    const pending = issueDraftPendingQuestion([
      assistant("m1", "Drafting.\n<issue_draft>{}"),
      assistant("m2", 'Which surfaces?\n<issue_draft_question>{"question":"Which surfaces?"}</issue_draft_question>'),
    ]);
    expect(pending?.messageId).toBe("m2");
    expect(pending?.question.question).toBe("Which surfaces?");
  });

  it("clears itself once the user answers", () => {
    // The user's own turn is the last message, so the chips disappear without
    // anything having to remember that they were clicked.
    const pending = issueDraftPendingQuestion([
      assistant("m2", '<issue_draft_question>{"question":"Which surfaces?"}</issue_draft_question>'),
      user("m3", "Settings and the issue list"),
    ]);
    expect(pending).toBeNull();
  });

  it("is null for a carrier reply that asks nothing, and for empty transcripts", () => {
    expect(issueDraftPendingQuestion([assistant("m1", "Done.")])).toBeNull();
    expect(issueDraftPendingQuestion([])).toBeNull();
  });
});

describe("issue draft input envelope", () => {
  it("round-trips the user's own words", () => {
    const encoded = encodeIssueDraftInput("add dark mode", {
      ...EMPTY,
      title: "Dark mode",
    });
    expect(decodeIssueDraftInput(encoded)).toBe("add dark mode");
  });

  it("carries the current draft so the carrier can preserve it", () => {
    const encoded = encodeIssueDraftInput("keep going", {
      ...EMPTY,
      title: "T",
      description: "D",
    });
    expect(JSON.parse(encoded.split("\n")[1]!)).toEqual({
      user_request: "keep going",
      current_draft: { title: "T", description: "D", status: "", priority: "" },
    });
  });

  it("returns anything that is not an envelope unchanged", () => {
    expect(decodeIssueDraftInput("plain text")).toBe("plain text");
    expect(decodeIssueDraftInput("MULTICA_ISSUE_DRAFT_INPUT\nnot json")).toBe(
      "MULTICA_ISSUE_DRAFT_INPUT\nnot json",
    );
  });
});

describe("mergeIssueDraftPayload", () => {
  it("overwrites only the fields the reply has an opinion about", () => {
    const current: IssueDraftPayload = {
      ...EMPTY,
      title: "Old",
      description: "Kept",
      priority: "high",
      project_id: "p1",
    };
    expect(mergeIssueDraftPayload(current, { title: "New" })).toEqual({
      ...current,
      title: "New",
    });
  });

  it("is a no-op when the reply had no parseable block", () => {
    expect(mergeIssueDraftPayload(EMPTY, null)).toBe(EMPTY);
  });
});

describe("issueDraftIsCreatable", () => {
  it("requires a title and nothing else", () => {
    expect(issueDraftIsCreatable({ ...EMPTY, title: "  " })).toBe(false);
    expect(issueDraftIsCreatable({ ...EMPTY, title: "T" })).toBe(true);
    expect(issueDraftIsCreatable({ ...EMPTY, title: "T", description: "" })).toBe(true);
  });
});
