// @vitest-environment node
import { describe, expect, it } from "vitest";
import { SquadListSchema, SquadMemberListSchema } from "@multica/core/api/schemas";
import type { Squad, SquadMember } from "@multica/core/types";
import {
  buildAgentSquadsMap,
  flattenAgentListItems,
  groupRowsBySquad,
  needsSquadMembersFetch,
  NO_SQUAD_ID,
  resolveSquadRosters,
  rowMatchesSquadFilter,
  squadFilterOptionCounts,
  type FetchedSquadMembers,
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

// Official-cloud GET /api/squads omits `members` (DENE-110 is fork-only).
// member_preview is capped at 3; the full roster is GET /api/squads/:id/members.
const GENERAL_AGENT_IDS = [
  "g-1",
  "g-2",
  "g-3",
  "g-4",
  "g-5",
  "g-6",
  "g-7",
] as const;
const GAME_AGENT_IDS = [
  "p-1",
  "p-2",
  "p-3",
  "p-4",
  "p-5",
  "p-6",
  "p-7",
] as const;
const MIKA_ID = "d715278a";
const GENERAL_SQUAD_ID = "sq-general";
const GAME_SQUAD_ID = "sq-game";

function preview3(agentIds: readonly string[]) {
  return agentIds.slice(0, 3).map((member_id) => ({
    member_type: "agent" as const,
    member_id,
    role: "member",
  }));
}

function officialCloudSquadListPayload() {
  return [
    {
      id: GENERAL_SQUAD_ID,
      workspace_id: "ws-1",
      name: "通用开发-Z战士",
      description: "",
      instructions: "",
      avatar_url: null,
      leader_id: "leader-1",
      creator_id: "user-1",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      archived_at: null,
      archived_by: null,
      member_count: 8,
      member_preview: preview3(GENERAL_AGENT_IDS),
    },
    {
      id: GAME_SQUAD_ID,
      workspace_id: "ws-1",
      name: "游戏专攻-Z战士",
      description: "",
      instructions: "",
      avatar_url: null,
      leader_id: "leader-1",
      creator_id: "user-1",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      archived_at: null,
      archived_by: null,
      member_count: 8,
      member_preview: preview3(GAME_AGENT_IDS),
    },
  ];
}

function membersResponse(
  squadId: string,
  agentIds: readonly string[],
  humanId: string,
): SquadMember[] {
  const created_at = "2026-01-01T00:00:00Z";
  return [
    ...agentIds.map((member_id, i) => ({
      id: `${squadId}-agent-${i}`,
      squad_id: squadId,
      member_type: "agent" as const,
      member_id,
      role: "member",
      created_at,
    })),
    {
      id: `${squadId}-human`,
      squad_id: squadId,
      member_type: "member" as const,
      member_id: humanId,
      role: "member",
      created_at,
    },
  ];
}

/** Matches ApiClient.listSquadMembers: schema failure is query error, not []. */
function fetchedFromMembersPayload(raw: unknown): FetchedSquadMembers {
  const parsed = SquadMemberListSchema.safeParse(raw);
  if (!parsed.success) return { status: "error" };
  return { status: "success", members: parsed.data };
}

function agentRowsFromMap(
  agentIds: readonly string[],
  map: Map<string, Squad[]>,
  membershipKnown = true,
) {
  return agentIds.map((id) => ({
    agent: { id },
    squadIds: (map.get(id) ?? []).map((s) => s.id),
    squadMembershipKnown: membershipKnown,
  }));
}

describe("official-cloud GET /api/squads payload (no members field)", () => {
  const allAgentIds = [...GENERAL_AGENT_IDS, ...GAME_AGENT_IDS, MIKA_ID];

  it("does not default a missing members field to an empty roster", () => {
    const parsed = SquadListSchema.parse(
      officialCloudSquadListPayload(),
    ) as Squad[];
    expect(parsed).toHaveLength(2);
    expect(parsed[0]?.member_count).toBe(8);
    expect(parsed[0]?.member_preview).toHaveLength(3);
    expect(parsed[0]?.members).toBeUndefined();
    expect(parsed[1]?.members).toBeUndefined();
  });

  it("fetches one members roster per active squad, not per agent", () => {
    const archived = makeSquad({
      id: "sq-old",
      name: "Archived",
      archived_at: "2026-02-01T00:00:00Z",
    });
    const squads = [
      ...(SquadListSchema.parse(officialCloudSquadListPayload()) as Squad[]),
      archived,
    ];
    const needingFetch = squads.filter(needsSquadMembersFetch);
    expect(needingFetch.map((s) => s.id)).toEqual([
      GENERAL_SQUAD_ID,
      GAME_SQUAD_ID,
    ]);
    expect(needingFetch).toHaveLength(2);
    expect(allAgentIds).toHaveLength(15);
  });

  it("maps 7 agents per squad and leaves only Mika unassigned once member fetches land", () => {
    const squads = SquadListSchema.parse(
      officialCloudSquadListPayload(),
    ) as Squad[];
    const fetched = new Map([
      [
        GENERAL_SQUAD_ID,
        {
          status: "success" as const,
          members: membersResponse(GENERAL_SQUAD_ID, GENERAL_AGENT_IDS, "kk"),
        },
      ],
      [
        GAME_SQUAD_ID,
        {
          status: "success" as const,
          members: membersResponse(GAME_SQUAD_ID, GAME_AGENT_IDS, "kk"),
        },
      ],
    ]);
    const rosters = resolveSquadRosters(squads, fetched);
    const map = buildAgentSquadsMap(squads, rosters);

    expect(
      GENERAL_AGENT_IDS.filter((id) =>
        map.get(id)?.some((s) => s.id === GENERAL_SQUAD_ID),
      ),
    ).toHaveLength(7);
    expect(
      GAME_AGENT_IDS.filter((id) =>
        map.get(id)?.some((s) => s.id === GAME_SQUAD_ID),
      ),
    ).toHaveLength(7);
    expect(map.get(MIKA_ID)).toBeUndefined();

    const rows = agentRowsFromMap(allAgentIds, map);
    expect(
      rows.filter((r) => rowMatchesSquadFilter(r.squadIds, [GENERAL_SQUAD_ID])),
    ).toHaveLength(7);
    expect(
      rows.filter((r) => rowMatchesSquadFilter(r.squadIds, [GAME_SQUAD_ID])),
    ).toHaveLength(7);
    expect(
      rows.filter((r) =>
        rowMatchesSquadFilter(r.squadIds, [NO_SQUAD_ID], r.squadMembershipKnown),
      ),
    ).toHaveLength(1);

    const groups = groupRowsBySquad(rows, squads, rosters);
    expect(groups.map((g) => g.id)).toEqual([
      GAME_SQUAD_ID,
      GENERAL_SQUAD_ID,
      NO_SQUAD_ID,
    ]);
    expect(groups[0]?.squad?.name).toBe("游戏专攻-Z战士");
    expect(groups[0]?.rows).toHaveLength(7);
    expect(groups[1]?.squad?.name).toBe("通用开发-Z战士");
    expect(groups[1]?.rows).toHaveLength(7);
    expect(groups[2]?.rows.map((r) => r.agent.id)).toEqual([MIKA_ID]);

    const items = flattenAgentListItems(rows, squads, "squad", rosters);
    expect(items.filter((item) => item.kind === "header").map((item) => item.key)).toEqual([
      `header:${GAME_SQUAD_ID}`,
      `header:${GENERAL_SQUAD_ID}`,
      `header:${NO_SQUAD_ID}`,
    ]);
  });

  it("does not render a 0 count or dump members into no-squad while a roster is unknown", () => {
    const squads = SquadListSchema.parse(
      officialCloudSquadListPayload(),
    ) as Squad[];
    const fetched = new Map([
      [GENERAL_SQUAD_ID, { status: "pending" as const }],
      [GAME_SQUAD_ID, { status: "error" as const }],
    ]);
    const rosters = resolveSquadRosters(squads, fetched);
    const map = buildAgentSquadsMap(squads, rosters);
    expect(map.size).toBe(0);

    const rows = agentRowsFromMap(allAgentIds, map, false);
    expect(
      rows.filter((r) =>
        rowMatchesSquadFilter(r.squadIds, [NO_SQUAD_ID], r.squadMembershipKnown),
      ),
    ).toHaveLength(0);

    const groups = groupRowsBySquad(rows, squads, rosters);
    expect(groups.map((g) => g.id)).toEqual([]);

    const counts = squadFilterOptionCounts(rows, squads, rosters);
    expect(counts.bySquadId.get(GENERAL_SQUAD_ID)).toBeNull();
    expect(counts.bySquadId.get(GAME_SQUAD_ID)).toBeNull();
    expect(counts.noSquadCount).toBeNull();
  });

  it("treats a schema-invalid members 2xx as unknown, not count 0 or no-squad", () => {
    const squads = SquadListSchema.parse(
      officialCloudSquadListPayload(),
    ) as Squad[];
    const fetched = new Map([
      [GENERAL_SQUAD_ID, fetchedFromMembersPayload({ members: [{ id: "x" }] })],
      [
        GAME_SQUAD_ID,
        fetchedFromMembersPayload([{ member_type: "agent" }]),
      ],
    ]);
    expect(fetched.get(GENERAL_SQUAD_ID)?.status).toBe("error");
    expect(fetched.get(GAME_SQUAD_ID)?.status).toBe("error");

    const rosters = resolveSquadRosters(squads, fetched);
    const map = buildAgentSquadsMap(squads, rosters);
    expect(map.size).toBe(0);

    const rows = agentRowsFromMap(allAgentIds, map, false);
    const counts = squadFilterOptionCounts(rows, squads, rosters);
    expect(counts.bySquadId.get(GENERAL_SQUAD_ID)).toBeNull();
    expect(counts.bySquadId.get(GAME_SQUAD_ID)).toBeNull();
    expect(counts.noSquadCount).toBeNull();

    const groups = groupRowsBySquad(rows, squads, rosters);
    expect(groups.map((g) => g.id)).not.toContain(NO_SQUAD_ID);
    expect(groups).toEqual([]);
  });

  it("prefers an inline members roster from a self-hosted fork and skips the fetch", () => {
    const forkSquad = makeSquad({
      id: GENERAL_SQUAD_ID,
      name: "通用开发-Z战士",
      member_count: 8,
      member_preview: preview3(GENERAL_AGENT_IDS),
      members: [
        ...GENERAL_AGENT_IDS.map((member_id) => ({
          member_type: "agent" as const,
          member_id,
          role: "member",
        })),
        { member_type: "member", member_id: "kk", role: "member" },
      ],
    });
    expect(needsSquadMembersFetch(forkSquad)).toBe(false);
    const map = buildAgentSquadsMap([forkSquad]);
    expect(
      GENERAL_AGENT_IDS.filter((id) => map.has(id)),
    ).toHaveLength(7);
  });
});
