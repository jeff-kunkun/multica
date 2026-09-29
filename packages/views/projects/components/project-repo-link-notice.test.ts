import { describe, expect, it } from "vitest";
import type { RepoBinding, RepoLink } from "@multica/core/types";
import { unmatchedConnectionScopes } from "./project-repo-link-notice";

function link(owner: string): RepoLink {
  return {
    id: `link-${owner}`,
    kind: "github_token",
    host: "github.com",
    owner,
    visibility: "workspace",
    health: "ok",
    can_manage: true,
  };
}

function binding(repoUrl: string, state: RepoBinding["state"], projectId?: string): RepoBinding {
  return {
    repo_url: repoUrl,
    state,
    can_configure: true,
    ...(projectId ? { source_projects: [{ id: projectId, title: "App" }] } : {}),
  };
}

describe("unmatchedConnectionScopes", () => {
  it("names a project repository whose account has no connection", () => {
    expect(
      unmatchedConnectionScopes(
        [link("jeff-kunkun")],
        [],
        [
          {
            resource_type: "github_repo",
            resource_ref: { url: "https://github.com/someone-else/app.git" },
          },
          {
            resource_type: "local_directory",
            resource_ref: { repo_key: "github.com/jeff-kunkun/multica" },
          },
        ],
        "project-1",
      ),
    ).toEqual(["github.com/someone-else"]);
  });

  it("includes a disconnected binding that still names this project", () => {
    expect(
      unmatchedConnectionScopes(
        [],
        [
          binding("https://github.com/acme/api", "disconnected", "project-1"),
          binding("https://github.com/other/api", "disconnected", "project-2"),
        ],
        [],
        "project-1",
      ),
    ).toEqual(["github.com/acme"]);
  });

  it("stays quiet when the account already resolves", () => {
    expect(
      unmatchedConnectionScopes(
        [link("acme")],
        [],
        [
          {
            resource_type: "github_repo",
            resource_ref: { url: "git@github.com:acme/api.git" },
          },
        ],
        "project-1",
      ),
    ).toEqual([]);
  });
});
