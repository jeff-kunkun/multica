// @vitest-environment node
import { describe, it, expect } from "vitest";
import {
  chatSessionProjectIds,
  toggleProjectId,
  replaceProjectId,
  sameProjectIds,
} from "./project-context";

describe("chatSessionProjectIds", () => {
  it("returns the set in selection order", () => {
    expect(chatSessionProjectIds({ project_ids: ["b", "a"], project_id: "b" })).toEqual([
      "b",
      "a",
    ]);
  });

  // A desktop build can outlive the server it talks to (and vice versa): a
  // response written before project_ids existed still names one project, and
  // dropping it would silently strip the chat's context chip.
  it("normalises a server that only sends the singular project_id", () => {
    expect(chatSessionProjectIds({ project_id: "a" })).toEqual(["a"]);
  });

  // The inverse of the case above, and the one that matters after a clear: a
  // response can carry an explicit empty set alongside a stale project_id.
  it("honours an explicit empty set over a leftover project_id", () => {
    expect(chatSessionProjectIds({ project_ids: [], project_id: "a" })).toEqual([]);
  });

  it("reads no context as an empty set", () => {
    expect(chatSessionProjectIds({ project_id: null })).toEqual([]);
    expect(chatSessionProjectIds(null)).toEqual([]);
    expect(chatSessionProjectIds(undefined)).toEqual([]);
  });

  it("copies, so a caller cannot mutate the cached row", () => {
    const session = { project_ids: ["a"], project_id: "a" };
    chatSessionProjectIds(session).push("b");
    expect(session.project_ids).toEqual(["a"]);
  });
});

describe("toggleProjectId", () => {
  it("appends a newly checked project so the head stays the primary one", () => {
    expect(toggleProjectId(["a"], "b")).toEqual(["a", "b"]);
  });

  it("removes a checked project and keeps the rest in order", () => {
    expect(toggleProjectId(["a", "b", "c"], "b")).toEqual(["a", "c"]);
  });

  it("does not mutate the input", () => {
    const before = ["a"];
    toggleProjectId(before, "b");
    expect(before).toEqual(["a"]);
  });
});

describe("replaceProjectId", () => {
  it("swaps one entry in place", () => {
    expect(replaceProjectId(["a", "b"], "a", "c")).toEqual(["c", "b"]);
  });

  // The server's unique index would collapse the duplicate, leaving the UI
  // claiming a position the stored set does not have.
  it("drops the replaced entry when the target is already attached", () => {
    expect(replaceProjectId(["a", "b"], "a", "b")).toEqual(["b"]);
  });

  it("is a no-op for an unattached source or an unchanged target", () => {
    expect(replaceProjectId(["a"], "z", "c")).toEqual(["a"]);
    expect(replaceProjectId(["a"], "a", "a")).toEqual(["a"]);
  });
});

describe("sameProjectIds", () => {
  it("compares order, because position 0 is the primary project", () => {
    expect(sameProjectIds(["a", "b"], ["a", "b"])).toBe(true);
    expect(sameProjectIds(["a", "b"], ["b", "a"])).toBe(false);
    expect(sameProjectIds(["a"], ["a", "b"])).toBe(false);
    expect(sameProjectIds([], [])).toBe(true);
  });
});
