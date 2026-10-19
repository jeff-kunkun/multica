// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  agentMagpieGroup,
  agentMagpieRoute,
  canPinMagpieGroup,
  agentMagpieGroups,
  agentMagpieRoutes,
  readRuntimeMagpieRoute,
  withAgentMagpieGroups,
} from "./magpie-route";

describe("readRuntimeMagpieRoute", () => {
  it("reads a routed CLI's group and models in order", () => {
    expect(
      readRuntimeMagpieRoute({
        magpie: {
          group: "office-claude",
          models: [
            { id: "claude/claude-opus-5-5", name: "Claude Opus 5.5" },
            { id: "claude/claude-sonnet-5-5" },
          ],
        },
      }),
    ).toEqual({
      group: "office-claude",
      models: [
        { id: "claude/claude-opus-5-5", name: "Claude Opus 5.5" },
        // No display name reported: the model id stands in.
        { id: "claude/claude-sonnet-5-5", name: "claude-sonnet-5-5" },
      ],
      groups: [],
    });
  });

  it("reads an unrouted CLI", () => {
    expect(
      readRuntimeMagpieRoute({ magpie: { group: "", models: [] } }),
    ).toEqual({ group: "", models: [], groups: [] });
  });

  it("shows nothing for an older daemon or a machine without magpie", () => {
    expect(readRuntimeMagpieRoute(undefined)).toBeNull();
    expect(readRuntimeMagpieRoute({})).toBeNull();
    expect(readRuntimeMagpieRoute({ cli_version: "0.7.0" })).toBeNull();
    expect(readRuntimeMagpieRoute({ magpie: null })).toBeNull();
  });

  it("shows nothing for a malformed route", () => {
    for (const magpie of [
      "office-claude",
      42,
      [],
      { group: "office-claude" },
      { group: "office-claude", models: [] },
      { group: "office-claude", models: "claude/claude-opus-5-5" },
      { group: "office-claude", models: [{ name: "no id" }] },
      { group: "office-claude", models: [{ id: "" }] },
      { group: 1, models: [] },
    ]) {
      expect(readRuntimeMagpieRoute({ magpie })).toBeNull();
    }
  });

  it("ignores fields it does not know", () => {
    expect(
      readRuntimeMagpieRoute({
        magpie: {
          group: "fast",
          routing: "order",
          models: [{ id: "a/one", name: "One", account: "x" }],
        },
      }),
    ).toEqual({
      group: "fast",
      models: [{ id: "a/one", name: "One" }],
      groups: [],
    });
  });

  it("reads the machine's groups and drops malformed ones alone", () => {
    expect(
      readRuntimeMagpieRoute({
        magpie: {
          group: "office-opencode",
          models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }],
          groups: [
            { name: "office-opencode", models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }] },
            { name: "office-pojia", models: [{ id: "nexus-gpu/nexus-coder" }] },
            { name: "", models: [{ id: "a/b" }] },
            { name: "empty", models: [] },
            "office-router",
          ],
        },
      })?.groups,
    ).toEqual([
      { name: "office-opencode", models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }] },
      { name: "office-pojia", models: [{ id: "nexus-gpu/nexus-coder", name: "nexus-coder" }] },
    ]);
  });
});

describe("agent magpie group", () => {
  it("reads a pinned group from OpenCode's --model", () => {
    expect(agentMagpieGroup("opencode", ["--model", "magpie/group/office-pojia"])).toBe("office-pojia");
    expect(agentMagpieGroup("opencode", ["--model=magpie/group/office-pojia"])).toBe("office-pojia");
    expect(agentMagpieGroup("opencode", ["-m", "magpie/group/office-pojia"])).toBe("office-pojia");
    expect(agentMagpieGroup("opencode", [])).toBeNull();
    expect(agentMagpieGroup("opencode", ["--model", "zhipu/glm-5.3"])).toBeNull();
    expect(agentMagpieGroup("opencode", ["--model"])).toBeNull();
    // Only CLIs whose group model the office knows can be pinned.
    expect(agentMagpieGroup("claude", ["--model", "magpie/group/office-pojia"])).toBeNull();
    expect(canPinMagpieGroup("opencode")).toBe(true);
    expect(canPinMagpieGroup("claude")).toBe(false);
  });

  it("shows the route the agent runs on", () => {
    const route = {
      group: "office-opencode",
      models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }],
      groups: [
        { name: "office-opencode", models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }] },
        { name: "office-pojia", models: [{ id: "nexus-gpu/nexus-coder", name: "nexus-coder" }] },
      ],
    };
    expect(agentMagpieRoute(route, "opencode", [])).toBe(route);
    expect(agentMagpieRoute(route, "opencode", ["--model", "magpie/group/office-pojia"])).toEqual({
      group: "office-pojia",
      models: [{ id: "nexus-gpu/nexus-coder", name: "nexus-coder" }],
      groups: route.groups,
    });
    // Pinned to a group the machine no longer lists: its name, no models.
    expect(agentMagpieRoute(route, "opencode", ["--model", "magpie/group/gone"])?.models).toEqual([]);
    expect(agentMagpieRoute(null, "opencode", ["--model", "magpie/group/office-pojia"])).toBeNull();
  });
});

describe("agent magpie groups", () => {
  const route = {
    group: "office-opencode",
    models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }],
    groups: [
      { name: "office-opencode", models: [{ id: "zhipu/glm-5.3", name: "GLM-5.3" }] },
      { name: "office-pojia", models: [{ id: "nexus-gpu/nexus-coder", name: "nexus-coder" }] },
    ],
  };

  it("reads the groups an agent lists, default first", () => {
    expect(agentMagpieGroups({ magpie_groups: ["office-pojia", " office-opencode ", "office-pojia", "", 3] })).toEqual([
      "office-pojia",
      "office-opencode",
    ]);
    expect(agentMagpieGroups({})).toEqual([]);
    expect(agentMagpieGroups({ magpie_groups: "office-pojia" })).toEqual([]);
    expect(agentMagpieGroups(null)).toEqual([]);
  });

  it("writes the groups and keeps other runtime config", () => {
    expect(withAgentMagpieGroups({ mode: "x" }, ["office-opencode", "office-pojia"])).toEqual({
      mode: "x",
      magpie_groups: ["office-opencode", "office-pojia"],
    });
    expect(withAgentMagpieGroups({ mode: "x", magpie_groups: ["a"] }, [])).toEqual({ mode: "x" });
  });

  it("lists every route an agent can run on", () => {
    const agent = (custom_args: string[], runtime_config: Record<string, unknown>) => ({ custom_args, runtime_config });
    expect(
      agentMagpieRoutes(route, "opencode", agent([], { magpie_groups: ["office-opencode", "office-pojia"] })).map(
        (r) => [r.group, r.models.map((m) => m.name)],
      ),
    ).toEqual([
      ["office-opencode", ["GLM-5.3"]],
      ["office-pojia", ["nexus-coder"]],
    ]);
    // No groups listed: the one route its custom args or CLI give it.
    expect(agentMagpieRoutes(route, "opencode", agent([], {}))).toEqual([route]);
    expect(
      agentMagpieRoutes(route, "opencode", agent(["--model", "magpie/group/office-pojia"], {}))[0]?.group,
    ).toBe("office-pojia");
    // A CLI the office cannot pin ignores the list.
    expect(agentMagpieRoutes(route, "claude", agent([], { magpie_groups: ["office-pojia"] }))).toEqual([route]);
    expect(agentMagpieRoutes(null, "opencode", agent([], {}))).toEqual([]);
  });
});
