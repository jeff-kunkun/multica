// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent, IssueDraftPayload, Squad } from "../types";
import {
  applyIssueDraftAssigneeSuggestions,
  buildIssueDraftAssigneeRoster,
  suggestIssueDraftAssignees,
  type IssueDraftAssigneeRosterEntry,
} from "./assignee-suggestion";

/**
 * Matching is scored against tags the workspace already put on the roster.
 * These fixtures follow the Multica 魔改 seats: function roles live on the
 * generic squad, direction is a name suffix / specialised squad, and
 * routing_tier is the strength ladder.
 */

function agent(overrides: Partial<Agent> & Pick<Agent, "id" | "name">): Agent {
  return {
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    permission_mode: "public_to",
    invocation_targets: [{ target_type: "workspace", target_id: null }],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: "user-1",
    skills: [],
    created_at: "",
    updated_at: "",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function squad(
  overrides: Partial<Squad> & Pick<Squad, "id" | "name" | "leader_id">,
): Squad {
  return {
    workspace_id: "ws-1",
    description: "",
    instructions: "",
    avatar_url: null,
    creator_id: "user-1",
    created_at: "",
    updated_at: "",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

const 贝吉塔 = agent({
  id: "ag-vegeta",
  name: "贝吉塔",
  routing_tier: "medium",
});
const 比克 = agent({
  id: "ag-piccolo",
  name: "比克",
  routing_tier: "weak",
});
const 龟仙人 = agent({
  id: "ag-roshi",
  name: "龟仙人",
  routing_tier: "strongest",
});
const 孙悟空 = agent({
  id: "ag-goku",
  name: "孙悟空",
  routing_tier: "strong",
});
const 孙悟空游戏 = agent({
  id: "ag-goku-game",
  name: "孙悟空游戏",
  routing_tier: "strong",
  parent_agent_id: "ag-goku",
  parent_agent_name: "孙悟空",
});
const 贝吉塔游戏 = agent({
  id: "ag-vegeta-game",
  name: "贝吉塔游戏",
  routing_tier: "medium",
  parent_agent_id: "ag-vegeta",
  parent_agent_name: "贝吉塔",
});

const genericSquad = squad({
  id: "sq-generic",
  name: "通用开发-Z战士",
  leader_id: "ag-goku",
  members: [
    { member_type: "agent", member_id: "ag-goku", role: "leader" },
    { member_type: "agent", member_id: "ag-vegeta", role: "实现（Builder）" },
    { member_type: "agent", member_id: "ag-piccolo", role: "网页验收（Operator）" },
    { member_type: "agent", member_id: "ag-roshi", role: "架构与把关（Architect）" },
    { member_type: "member", member_id: "user-1", role: "决策人" },
  ],
});

const gameSquad = squad({
  id: "sq-game",
  name: "游戏专攻-Z战士",
  leader_id: "ag-goku-game",
  members: [
    { member_type: "agent", member_id: "ag-goku-game", role: "leader" },
    { member_type: "agent", member_id: "ag-vegeta-game", role: "member" },
  ],
});

const roster = buildIssueDraftAssigneeRoster({
  agents: [贝吉塔, 比克, 龟仙人, 孙悟空, 孙悟空游戏, 贝吉塔游戏],
  squads: [genericSquad, gameSquad],
});

function payload(overrides: Partial<IssueDraftPayload> = {}): IssueDraftPayload {
  return {
    title: "Parent",
    description: "",
    status: "",
    priority: "",
    ...overrides,
  };
}

function suggestedId(
  entries: readonly IssueDraftAssigneeRosterEntry[],
  hint: string,
  extra: Partial<IssueDraftPayload> = {},
): string | null {
  const result = suggestIssueDraftAssignees(
    payload({
      title: extra.title ?? "Parent",
      description: extra.description ?? "",
      children: [
        {
          key: "c1",
          title: extra.children?.[0]?.title ?? "Child",
          description: "",
          status: "",
          priority: "",
          assignee_hint: hint,
        },
      ],
      ...extra,
    }),
    entries,
  );
  return result.children.c1?.assigneeId ?? null;
}

describe("buildIssueDraftAssigneeRoster", () => {
  it("tags a seat with its roster role and routing tier, not generic roles", () => {
    const vegeta = roster.find((entry) => entry.assigneeId === "ag-vegeta");
    expect(vegeta?.tags).toEqual(
      expect.arrayContaining(["实现", "builder", "medium", "中"]),
    );
    const goku = roster.find((entry) => entry.assigneeId === "ag-goku");
    expect(goku?.tags).not.toContain("leader");
    expect(goku?.tags).toEqual(expect.arrayContaining(["strong", "强"]));
  });

  it("lets a specialisation inherit its parent's function role", () => {
    const vegetaGame = roster.find((entry) => entry.assigneeId === "ag-vegeta-game");
    expect(vegetaGame?.parentAgentId).toBe("ag-vegeta");
    expect(vegetaGame?.tags).toEqual(
      expect.arrayContaining(["实现", "builder", "游戏"]),
    );
  });

  it("drops archived agents and ignores human roster rows", () => {
    const built = buildIssueDraftAssigneeRoster({
      agents: [
        贝吉塔,
        agent({ id: "ag-old", name: "旧席", archived_at: "2026-01-01T00:00:00Z" }),
      ],
      squads: [genericSquad],
    });
    expect(built.map((entry) => entry.assigneeId)).toEqual(["ag-vegeta"]);
  });
});

describe("suggestIssueDraftAssignees", () => {
  it("maps the carrier's work-kind hint onto the roster role", () => {
    expect(suggestedId(roster, "backend implementation")).toBe("ag-vegeta");
    expect(suggestedId(roster, "frontend page")).toBe("ag-vegeta");
    expect(suggestedId(roster, "manual verification")).toBe("ag-piccolo");
    expect(suggestedId(roster, "architecture review")).toBe("ag-roshi");
  });

  it("prefers the direction specialisation when the draft names one", () => {
    expect(
      suggestedId(roster, "backend implementation", { title: "游戏内购修复" }),
    ).toBe("ag-vegeta-game");
  });

  it("prefers the base seat when several specialisations match equally", () => {
    expect(suggestedId(roster, "backend implementation")).toBe("ag-vegeta");
  });

  it("leaves the slot unassigned when nothing unique matches", () => {
    expect(suggestedId(roster, "something unnamed")).toBeNull();
    expect(suggestedId([], "backend implementation")).toBeNull();
  });

  it("uses the parent title as the parent's hint", () => {
    const result = suggestIssueDraftAssignees(
      payload({ title: "架构把关：模块边界", children: [] }),
      roster,
    );
    expect(result.parent?.assigneeId).toBe("ag-roshi");
  });

  it("falls back to the child title when assignee_hint is empty", () => {
    const result = suggestIssueDraftAssignees(
      payload({
        children: [
          {
            key: "c1",
            title: "网页验收与截图",
            description: "",
            status: "",
            priority: "",
            assignee_hint: null,
          },
        ],
      }),
      roster,
    );
    expect(result.children.c1?.assigneeId).toBe("ag-piccolo");
  });
});

describe("applyIssueDraftAssigneeSuggestions", () => {
  const suggestions = {
    parent: { assigneeType: "agent" as const, assigneeId: "ag-goku" },
    children: {
      c1: { assigneeType: "agent" as const, assigneeId: "ag-vegeta" },
    },
  };

  it("fills empty slots and leaves filled ones alone", () => {
    const next = applyIssueDraftAssigneeSuggestions(
      payload({
        children: [
          {
            key: "c1",
            title: "Backend",
            description: "",
            status: "",
            priority: "",
            assignee_id: null,
          },
        ],
      }),
      suggestions,
    );
    expect(next.assignee_id).toBe("ag-goku");
    expect(next.children?.[0]?.assignee_id).toBe("ag-vegeta");

    const kept = applyIssueDraftAssigneeSuggestions(
      payload({
        assignee_type: "agent",
        assignee_id: "ag-picked",
        children: [
          {
            key: "c1",
            title: "Backend",
            description: "",
            status: "",
            priority: "",
            assignee_type: "agent",
            assignee_id: "ag-picked-child",
          },
        ],
      }),
      suggestions,
    );
    expect(kept.assignee_id).toBe("ag-picked");
    expect(kept.children?.[0]?.assignee_id).toBe("ag-picked-child");
  });

  it("does not fill keys the caller asked to skip", () => {
    const next = applyIssueDraftAssigneeSuggestions(
      payload({
        children: [
          {
            key: "c1",
            title: "Backend",
            description: "",
            status: "",
            priority: "",
          },
        ],
      }),
      suggestions,
      { skipKeys: new Set(["", "c1"]) },
    );
    expect(next.assignee_id ?? null).toBeNull();
    expect(next.children?.[0]?.assignee_id ?? null).toBeNull();
  });

  it("returns the same object when nothing changed", () => {
    const original = payload();
    expect(
      applyIssueDraftAssigneeSuggestions(original, {
        parent: null,
        children: {},
      }),
    ).toBe(original);
  });
});
