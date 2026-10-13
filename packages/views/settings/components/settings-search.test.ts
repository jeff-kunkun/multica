// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  descriptionSnippet,
  searchSettings,
  type SettingsSearchEntry,
} from "./settings-search";

const entries: SettingsSearchEntry[] = [
  { tab: "profile", title: "Profile" },
  {
    tab: "profile",
    anchor: "about",
    title: "About you",
    description: "Given to agents working for you on every run.",
  },
  { tab: "workspace", anchor: "context", title: "Agent context" },
  { tab: "code", anchor: "repositories", title: "Repositories" },
  { tab: "code", anchor: "co-author", title: "Co-author" },
];

describe("searchSettings", () => {
  it("returns nothing for an empty or blank query", () => {
    expect(searchSettings(entries, "")).toEqual([]);
    expect(searchSettings(entries, "   ")).toEqual([]);
  });

  it("ranks title matches before description-only matches", () => {
    const results = searchSettings(entries, "agent");
    expect(results.map((result) => result.title)).toEqual([
      "Agent context",
      "About you",
    ]);
  });

  it("matches case-insensitively and keeps each anchor on a shared page", () => {
    expect(searchSettings(entries, "REPOSITORIES").map((r) => r.anchor)).toEqual([
      "repositories",
    ]);
    // Anchors share a tab, so the anchor is part of the identity.
    expect(searchSettings(entries, "co").map((r) => r.title)).toEqual([
      "Agent context",
      "Co-author",
    ]);
  });

  it("keeps the first entry for a duplicated location", () => {
    const results = searchSettings(
      [...entries, { tab: "profile", title: "Profile again" }],
      "profile",
    );
    expect(results.map((r) => r.title)).toEqual(["Profile"]);
  });
});

describe("descriptionSnippet", () => {
  it("keeps short descriptions whole", () => {
    expect(descriptionSnippet("Given to agents.", "agents")).toBe(
      "Given to agents.",
    );
  });

  it("trims the lead so a late match stays visible", () => {
    const text =
      "This is a rather long description that eventually mentions agents.";
    const snippet = descriptionSnippet(text, "agents");
    expect(snippet.startsWith("...")).toBe(true);
    expect(snippet).toContain("agents");
    expect(snippet.length).toBeLessThan(text.length);
  });
});
