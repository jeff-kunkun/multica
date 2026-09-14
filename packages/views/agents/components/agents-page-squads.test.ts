// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Squad } from "@multica/core/types";
import {
  buildAgentSquadsMap,
  flattenAgentListItems,
  groupRowsBySquad,
  NO_SQUAD_ID,
  rowMatchesSquadFilter,
} from "./agents-page-squads";

function makeSquad(
  over: Partial<Squad> & Pick<Squad, "id" | "name">,
): Squad {
  return {
    workspace_id: "ws-1",
    description: "",
    instructions: "",
    avatar_url: null,
    leader_id: "leader-1",
    creator_id: "user-1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    members: [],
    ...over,
  };
}

function row(id: string, squadIds: string[] = []) {
  return { agent: { id }, squadIds };
}

describe("buildAgentSquadsMap", () => {
  it("maps agent members of active squads, skipping humans and archived squads", () => {
    const alpha = makeSquad({
      id: "sq-alpha",
      name: "Alpha",
      members: [
        { member_type: "agent", member_id: "a-1", role: "member" },
        { member_type: "member", member_id: "u-1", role: "member" },
      ],
    });
    const archived = makeSquad({
      id: "sq-old",
      name: "Old",
      archived_at: "2026-02-01T00:00:00Z",
      members: [{ member_type: "agent", member_id: "a-1", role: "member" }],
    });
    const map = buildAgentSquadsMap([alpha, archived]);
    expect(map.get("a-1")?.map((s) => s.id)).toEqual(["sq-alpha"]);
    expect(map.get("u-1")).toBeUndefined();
  });
});

describe("rowMatchesSquadFilter", () => {
  it("treats an empty selection as inactive", () => {
    expect(rowMatchesSquadFilter(["sq-1"], [])).toBe(true);
    expect(rowMatchesSquadFilter([], [])).toBe(true);
  });
});

describe("groupRowsBySquad", () => {
  const alpha = makeSquad({
    id: "sq-alpha",
    name: "Alpha",
    members: [
      { member_type: "agent", member_id: "a-1", role: "member" },
      { member_type: "agent", member_id: "a-2", role: "member" },
    ],
  });
  const beta = makeSquad({
    id: "sq-beta",
    name: "Beta",
    members: [{ member_type: "agent", member_id: "a-2", role: "member" }],
  });

  it("puts multi-squad agents in every matching group and unassigned last", () => {
    const rows = [
      row("a-1", ["sq-alpha"]),
      row("a-2", ["sq-alpha", "sq-beta"]),
      row("a-3", []),
    ];
    const groups = groupRowsBySquad(rows, [beta, alpha]);
    expect(groups.map((g) => g.id)).toEqual([
      "sq-alpha",
      "sq-beta",
      NO_SQUAD_ID,
    ]);
    expect(groups[0]?.rows.map((r) => r.agent.id)).toEqual(["a-1", "a-2"]);
    expect(groups[1]?.rows.map((r) => r.agent.id)).toEqual(["a-2"]);
    expect(groups[2]?.rows.map((r) => r.agent.id)).toEqual(["a-3"]);
  });

  it("omits squads with no matching rows after filters", () => {
    const groups = groupRowsBySquad([row("a-3", [])], [alpha, beta]);
    expect(groups.map((g) => g.id)).toEqual([NO_SQUAD_ID]);
  });
});

describe("flattenAgentListItems", () => {
  it("returns a flat row list when grouping is none", () => {
    const items = flattenAgentListItems(
      [row("a-1", ["sq-alpha"]), row("a-2", [])],
      [],
      "none",
    );
    expect(items.map((item) => item.kind)).toEqual(["row", "row"]);
    expect(items.map((item) => item.key)).toEqual(["a-1", "a-2"]);
  });

  it("prefixes headers and unique keys when grouping by squad", () => {
    const squad = makeSquad({
      id: "sq-alpha",
      name: "Alpha",
      members: [{ member_type: "agent", member_id: "a-1", role: "member" }],
    });
    const items = flattenAgentListItems(
      [row("a-1", ["sq-alpha"]), row("a-2", [])],
      [squad],
      "squad",
    );
    expect(items.map((item) => item.kind)).toEqual([
      "header",
      "row",
      "header",
      "row",
    ]);
    expect(items.map((item) => item.key)).toEqual([
      "header:sq-alpha",
      "sq-alpha:a-1",
      `header:${NO_SQUAD_ID}`,
      `${NO_SQUAD_ID}:a-2`,
    ]);
  });
});
