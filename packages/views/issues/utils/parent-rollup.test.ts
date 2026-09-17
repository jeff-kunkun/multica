// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parentPipelineState, type ParentChildRollup } from "./parent-rollup";

const rollup = (over: Partial<ParentChildRollup>): ParentChildRollup => ({
  done: 0,
  total: 0,
  blocked: 0,
  active: 0,
  ...over,
});

describe("parentPipelineState", () => {
  it("is settled with no children at all", () => {
    expect(parentPipelineState(undefined)).toBe("settled");
    expect(parentPipelineState(rollup({}))).toBe("settled");
  });

  it("is settled once every child is finished", () => {
    expect(parentPipelineState(rollup({ total: 3, done: 3 }))).toBe("settled");
  });

  it("reports blocked ahead of everything else", () => {
    // A parent with work moving AND work stuck is a stuck parent: the running
    // child cannot make the blocked one land.
    expect(
      parentPipelineState(rollup({ total: 4, done: 1, blocked: 1, active: 2 })),
    ).toBe("blocked");
  });

  it("reports blocked even when the counts say everything is done", () => {
    // `done` counts `cancelled` too, so a cancelled sibling can fill the ring
    // while a real child sits blocked. The warning must survive that.
    expect(
      parentPipelineState(rollup({ total: 2, done: 2, blocked: 1 })),
    ).toBe("blocked");
  });

  it("separates a running pipeline from an unclaimed queue", () => {
    expect(parentPipelineState(rollup({ total: 3, done: 1, active: 1 }))).toBe(
      "active",
    );
    expect(parentPipelineState(rollup({ total: 3, done: 1 }))).toBe("stalled");
  });
});
