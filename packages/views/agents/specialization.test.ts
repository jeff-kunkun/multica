// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent, AgentSkillSummary } from "@multica/core/types";
import {
  activeChildrenOf,
  baseRoleOptions,
  childrenOf,
  composeEffectiveInstructions,
  hasInheritedPrompt,
  inheritedSkillChips,
  isBaseRole,
  isSpecialization,
  specializationCount,
} from "./specialization";

function agent(overrides: Partial<Agent>): Agent {
  return {
    id: "agent",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: null,
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function skill(id: string, name = id): AgentSkillSummary {
  return { id, name, description: "" };
}

describe("specialization relationship", () => {
  it("treats an absent parent field exactly like an empty one", () => {
    // Older backends omit the field entirely; that must read as a base role
    // rather than "unknown", which is what the flat list assumes.
    expect(isSpecialization(agent({}))).toBe(false);
    expect(isSpecialization(agent({ parent_agent_id: "" }))).toBe(false);
    expect(isSpecialization(agent({ parent_agent_id: "base-1" }))).toBe(true);
    expect(isBaseRole(agent({}))).toBe(true);
    expect(isBaseRole(agent({ parent_agent_id: "base-1" }))).toBe(false);
  });

  it("selects children by parent id, keeping archived ones distinguishable", () => {
    const agents = [
      agent({ id: "base-1", name: "Base" }),
      agent({ id: "child-1", parent_agent_id: "base-1" }),
      agent({
        id: "child-2",
        parent_agent_id: "base-1",
        archived_at: "2026-02-01T00:00:00Z",
      }),
      agent({ id: "child-3", parent_agent_id: "base-2" }),
    ];
    expect(childrenOf(agents, "base-1").map((a) => a.id)).toEqual([
      "child-1",
      "child-2",
    ]);
    expect(activeChildrenOf(agents, "base-1").map((a) => a.id)).toEqual([
      "child-1",
    ]);
    expect(childrenOf(agents, "")).toEqual([]);
  });

  it("counts a base role from child_count, falling back to visible children", () => {
    // child_count is workspace-wide (and active-only), so it stays right when
    // filters hide some children.
    expect(specializationCount({ parent_agent_id: "", child_count: 3 }, 1)).toBe(
      3,
    );
    expect(specializationCount({ parent_agent_id: "", child_count: 0 }, 2)).toBe(
      0,
    );
    // Absent (older backend, or a payload that failed that one field).
    expect(specializationCount({ parent_agent_id: "" }, 2)).toBe(2);
    // Never counts a specialisation's own (impossible) children.
    expect(
      specializationCount({ parent_agent_id: "base-1", child_count: 4 }, 0),
    ).toBe(0);
  });

  it("offers live base roles only, sorted by name", () => {
    const agents = [
      agent({ id: "b-z", name: "Zeta" }),
      agent({ id: "b-a", name: "Alpha" }),
      agent({ id: "child", name: "Child", parent_agent_id: "b-a" }),
      agent({
        id: "b-archived",
        name: "Archived",
        archived_at: "2026-02-01T00:00:00Z",
      }),
    ];
    expect(baseRoleOptions(agents).map((a) => a.id)).toEqual(["b-a", "b-z"]);
  });
});

describe("composeEffectiveInstructions", () => {
  it("mirrors the server's parent + blank line + child rule", () => {
    expect(composeEffectiveInstructions("parent", "child")).toBe(
      "parent\n\nchild",
    );
  });

  it("never leaves a stray separator when one side is empty", () => {
    // The claim path hands this exact string to the daemon; an extra blank
    // line here is a prompt the agent actually receives.
    expect(composeEffectiveInstructions("", "child")).toBe("child");
    expect(composeEffectiveInstructions("parent", "")).toBe("parent");
    expect(composeEffectiveInstructions(undefined, "child")).toBe("child");
    expect(composeEffectiveInstructions("parent", undefined)).toBe("parent");
    expect(composeEffectiveInstructions("", "")).toBe("");
  });

  it("does not trim the halves — they are shown verbatim", () => {
    // The preview must show the same bytes the server composes; trimming here
    // would make it disagree with what the daemon receives.
    expect(composeEffectiveInstructions("parent\n", "\nchild")).toBe(
      "parent\n\n\n\nchild",
    );
  });
});

describe("inherited skills", () => {
  it("drops skills the child also holds, and duplicates", () => {
    const chips = inheritedSkillChips({
      inherited_skills: [skill("a"), skill("b"), skill("a")],
      skills: [skill("a", "own copy")],
    });
    expect(chips.map((s) => s.id)).toEqual(["b"]);
  });

  it("is empty when the backend does not serve the inherited list", () => {
    expect(inheritedSkillChips({ skills: [skill("a")] })).toEqual([]);
  });
});

describe("hasInheritedPrompt", () => {
  it("needs both a parent and readable text", () => {
    expect(
      hasInheritedPrompt({
        parent_agent_id: "base-1",
        inherited_instructions: "parent prompt",
      }),
    ).toBe(true);
    // Parent set but text empty: either the base role has no prompt or this
    // viewer may not read it. Neither may be rendered as a parent half.
    expect(
      hasInheritedPrompt({ parent_agent_id: "base-1", inherited_instructions: "" }),
    ).toBe(false);
    expect(hasInheritedPrompt({ parent_agent_id: "" })).toBe(false);
  });
});
