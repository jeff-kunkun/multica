// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  agentSceneOf,
  domainFit,
  groupAgentsByFit,
  resolveSceneDomains,
  sortAgentsByFit,
} from "./domain-fit";

// The same table server/internal/routing/domainfit_test.go reads (DENE-1477).
// It speaks in domain names; the client compares ids, the rule only compares.
const table = JSON.parse(
  readFileSync(new URL("./domain-fit.cases.json", import.meta.url), "utf8"),
) as {
  scenes: { name: string; issue_domain: string; project_domains: string[]; want: string[] }[];
  cases: { name: string; scene: string[]; agent: { name: string; domain: string }; fit: string }[];
};

describe("domain-fit case table", () => {
  it.each(table.scenes)("scene: $name", (s) => {
    expect(resolveSceneDomains(s.issue_domain, s.project_domains)).toEqual(s.want);
  });

  it.each(table.cases)("fit: $name", (c) => {
    expect(domainFit(c.scene, c.agent.domain)).toBe(c.fit);
  });
});

const agent = (id: string, domain?: string) => ({ id, domain_id: domain });
const project = (id: string, domains: string[]) => ({ id, title: id, domain_ids: domains });

describe("agentSceneOf", () => {
  it("no project is no scene", () => {
    expect(agentSceneOf([])).toBeNull();
  });

  it("unions project domains, the issue's own domain wins", () => {
    const ps = [project("a", ["d1"]), project("b", ["d2", "d1"])];
    expect(agentSceneOf(ps)?.domains).toEqual(["d1", "d2"]);
    expect(agentSceneOf(ps, "d2")?.domains).toEqual(["d2"]);
  });

  it("a project with no domain is a generic scene, not no scene", () => {
    expect(agentSceneOf([project("m", [])])?.domains).toEqual([]);
  });
});

describe("groupAgentsByFit", () => {
  const agents = [
    agent("base"),
    agent("game", "d-game"),
    agent("out", "d-out"),
    agent("base2"),
  ];

  it("domain scene: fit, then base roles, then other domains, order kept", () => {
    const scene = agentSceneOf([project("tarot", ["d-out"])])!;
    expect(groupAgentsByFit(agents, scene).map((g) => [g.fit, g.items.map((a) => a.id)])).toEqual([
      ["match", ["out"]],
      ["generic", ["base", "base2"]],
      ["other", ["game"]],
    ]);
  });

  it("generic scene: base roles fit, specialisations last, no empty group", () => {
    const scene = agentSceneOf([project("m", [])])!;
    expect(groupAgentsByFit(agents, scene).map((g) => g.fit)).toEqual(["match", "other"]);
    expect(sortAgentsByFit(agents, scene).map((a) => a.id)).toEqual(["base", "base2", "game", "out"]);
  });
});
