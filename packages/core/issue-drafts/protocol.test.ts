// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { IssueDraftPayload } from "../types";
import {
  decodeIssueDraftInput,
  encodeIssueDraftInput,
  issueDraftIsCreatable,
  mergeIssueDraftPayload,
  parseIssueDraftBlock,
  stripIssueDraftBlock,
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

describe("stripIssueDraftBlock", () => {
  it("removes complete and still-streaming blocks", () => {
    expect(stripIssueDraftBlock('Done.\n<issue_draft>{"title":"T"}</issue_draft>')).toBe(
      "Done.",
    );
    expect(stripIssueDraftBlock('Working…\n<issue_draft>{"title":"T"')).toBe("Working…");
  });

  it("leaves a reply with no block untouched", () => {
    expect(stripIssueDraftBlock("Just a question?")).toBe("Just a question?");
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
