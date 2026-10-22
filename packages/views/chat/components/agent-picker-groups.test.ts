import { describe, expect, it } from "vitest";
import type { Agent } from "@multica/core/types";
import { groupAgentsForPicker } from "./agent-picker-groups";

const agent = (id: string, owner: string) => ({ id, name: id, owner_id: owner }) as Agent;

const agents = [agent("base", "me"), agent("out-a", "other"), agent("media-a", "me")];
const ids = (g: { agents: Agent[] }) => g.agents.map((a) => a.id);

describe("groupAgentsForPicker", () => {
  it("splits mine / others, keeping order", () => {
    const g = groupAgentsForPicker({ agents, userId: "me" });
    expect(g.map((x) => x.kind)).toEqual(["mine", "others"]);
    expect(ids(g[0]!)).toEqual(["base", "media-a"]);
    expect(ids(g[1]!)).toEqual(["out-a"]);
  });

  it("search runs over everyone and drops empty groups", () => {
    const g = groupAgentsForPicker({ agents, userId: "me", query: "out" });
    expect(g.map((x) => x.kind)).toEqual(["others"]);
    expect(ids(g[0]!)).toEqual(["out-a"]);
  });
});
