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

  it("reads a block the model wrote after prose, with Markdown in the fields", () => {
    // The carrier's real replies put the block last, after blank-line-separated
    // prose, with escaped newlines and quotes inside the JSON strings.
    const reply = [
      "先给一版可执行的草稿，再问你一个最影响实现的问题。",
      "",
      "**最关键的一点：批量的“范围”是什么？**",
      "",
      '<issue_draft>{"title":"收件箱支持批量标记已读","description":"## 问题\\n\\n收件箱目前只能逐条标记已读。\\n\\n## 验收标准\\n- 支持「全选」\\n- 操作幂等","status":"","priority":""}</issue_draft>',
    ].join("\n");
    expect(parseIssueDraftBlock(reply)).toEqual({
      title: "收件箱支持批量标记已读",
      description:
        "## 问题\n\n收件箱目前只能逐条标记已读。\n\n## 验收标准\n- 支持「全选」\n- 操作幂等",
    });
  });

  it("reads a block that follows the guided question block", () => {
    const reply =
      'Who should run it?\n<issue_draft_question>{"question":"Who should run it?"}</issue_draft_question>\n<issue_draft>{"title":"Dark mode"}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({ title: "Dark mode" });
    expect(parseIssueDraftQuestion(reply)?.question).toBe("Who should run it?");
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

  /**
   * The shapes below are the ones the CLI-backed carriers actually produced in
   * the DENE-317 acceptance run: prose paragraphs, a blank line, then one block
   * whose JSON holds real Markdown with escaped newlines, sometimes with a
   * trailing blank line, sometimes with the guided question block above it.
   * They are here because a strip that only handles the tidy one-line fixture
   * is exactly how the raw block reached the transcript.
   */
  it("strips the block out of a reply written as prose, blank line, block", () => {
    const reply = [
      "两处已落进草稿：分段头只显示任务数，开关默认关闭已写死为验收项。",
      "",
      "标题保留你人工校订后的版本；描述字段这次传过来是空的，我把上一版内容带回并合并了这两点。",
      "",
      '<issue_draft>{"title":"任务看板支持按周分组视图（人工校订）","description":"## 问题\\n\\n当前任务看板只能按状态（列）查看。\\n\\n## 验收标准\\n- 周起始为周一","status":"","priority":""}</issue_draft>',
    ].join("\n");
    expect(stripIssueDraftDirectives(reply)).toBe(
      [
        "两处已落进草稿：分段头只显示任务数，开关默认关闭已写死为验收项。",
        "",
        "标题保留你人工校订后的版本；描述字段这次传过来是空的，我把上一版内容带回并合并了这两点。",
      ].join("\n"),
    );
  });

  it("strips a block that has prose after it, and one with a trailing blank line", () => {
    expect(
      stripIssueDraftDirectives(
        'Drafting now.\n<issue_draft>{"title":"T"}</issue_draft>\nAnything else?',
      ),
    ).toBe("Drafting now.\nAnything else?");
    expect(
      stripIssueDraftDirectives('已为您更新需求草稿：\n\n<issue_draft>{"title":"T"}</issue_draft>\n'),
    ).toBe("已为您更新需求草稿：");
  });

  it("strips the question block the guided policy puts above the draft block", () => {
    const reply =
      'Who should run it?\n<issue_draft_question>{"question":"Who should run it?","options":[{"label":"A bot","value":"Assign a bot","recommended":true}]}</issue_draft_question>\n<issue_draft>{"title":"Dark mode"}</issue_draft>';
    expect(stripIssueDraftDirectives(reply)).toBe("Who should run it?");
  });

  it("strips an unterminated block that follows prose, and an unterminated question block", () => {
    // A reply still streaming, or one the model never closed: whatever follows
    // the opening tag is machine-readable and must not reach the screen.
    expect(
      stripIssueDraftDirectives(
        'Prose first.\n<issue_draft>{"title":"T","description":"still arr',
      ),
    ).toBe("Prose first.");
    expect(
      stripIssueDraftDirectives('Which surface?\n<issue_draft_question>{"question":"Which'),
    ).toBe("Which surface?");
  });

  it("strips blocks written with CRLF line endings", () => {
    expect(
      stripIssueDraftDirectives('Done.\r\n<issue_draft>{"title":"T"}</issue_draft>\r\n'),
    ).toBe("Done.");
  });

  it("reduces a reply that was nothing but a block to nothing", () => {
    expect(stripIssueDraftDirectives('<issue_draft>{"title":"T"}</issue_draft>')).toBe("");
    expect(stripIssueDraftDirectives('\n<issue_draft>{"title":"T"}</issue_draft>\n')).toBe("");
  });

  it("keeps the block's own markup out while leaving the prose's markup alone", () => {
    // The JSON quotes `##` headings and code spans; only the block is removed,
    // so the surrounding Markdown still renders as the carrier wrote it.
    const reply =
      '一版草稿：\n\n- **范围**：仅勾选可见条目\n\n<issue_draft>{"description":"## 验收标准\\n- 使用 `due date` 字段"}</issue_draft>';
    expect(stripIssueDraftDirectives(reply)).toBe(
      "一版草稿：\n\n- **范围**：仅勾选可见条目",
    );
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
