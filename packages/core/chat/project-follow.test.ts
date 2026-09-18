// @vitest-environment node
import { describe, it, expect } from "vitest";
import { planFollowedProjectIds, type ProjectFollowInput } from "./project-follow";

function input(overrides: Partial<ProjectFollowInput> = {}): ProjectFollowInput {
  return {
    routeProjectId: "p1",
    selectedProjectIds: [],
    locked: false,
    hasSession: false,
    projectsLoaded: true,
    ...overrides,
  };
}

describe("planFollowedProjectIds", () => {
  it("binds the route's project while drafting", () => {
    expect(planFollowedProjectIds(input())).toEqual(["p1"]);
  });

  it("replaces a previously followed project when the route moves", () => {
    expect(planFollowedProjectIds(input({ selectedProjectIds: ["p0"] }))).toEqual(["p1"]);
  });

  it("does nothing when the draft already follows the route", () => {
    expect(planFollowedProjectIds(input({ selectedProjectIds: ["p1"] }))).toBeNull();
  });

  it("never rewrites an open session's server-owned set", () => {
    expect(planFollowedProjectIds(input({ hasSession: true }))).toBeNull();
  });

  it("stops following once the user picked a project inside the chat", () => {
    expect(
      planFollowedProjectIds(input({ locked: true, selectedProjectIds: ["p2"] })),
    ).toBeNull();
  });

  it("waits for the project list", () => {
    expect(planFollowedProjectIds(input({ projectsLoaded: false }))).toBeNull();
  });

  it("keeps the current set on a route that names no project", () => {
    expect(
      planFollowedProjectIds(input({ routeProjectId: null, selectedProjectIds: ["p1"] })),
    ).toBeNull();
  });
});
