import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { ApiClient, ApiError } from "./client";
import { parseWithFallback } from "./schema";

// Helper: stub fetch with a single JSON response. Status defaults to 200.
function stubFetchJson(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(typeof body === "string" ? body : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

// These tests cover the five failure modes that white-screened the desktop
// app in past incidents. The contract is: a malformed response degrades to
// an empty/safe shape, never throws into React.
describe("ApiClient schema fallback", () => {
  describe("GitHub repository import", () => {
    it("falls back safely when installation or repository responses are malformed", async () => {
      stubFetchJson({ installations: "not-an-array", configured: true });
      const client = new ApiClient("https://api.example.test");
      await expect(client.listGitHubInstallations("ws-1")).resolves.toEqual({
        installations: [],
        configured: false,
        repository_browse_configured: false,
        can_manage: false,
      });

      stubFetchJson({ repositories: [{ id: "wrong-type" }] });
      await expect(
        client.listGitHubInstallationRepositories("ws-1", "installation-1"),
      ).resolves.toEqual({
        repositories: [],
        total_count: 0,
        next_page: null,
      });
    });

    it("adds the allowlisted repository return target to the connect request", async () => {
      stubFetchJson({
        configured: true,
        url: "https://github.com/apps/multica/installations/new",
      });
      const client = new ApiClient("https://api.example.test");

      await client.getGitHubConnectURL("ws-1", "repositories");

      const fetchMock = vi.mocked(fetch);
      expect(fetchMock).toHaveBeenCalledWith(
        "https://api.example.test/api/workspaces/ws-1/github/connect?return_to=repositories",
        expect.any(Object),
      );
    });
  });

  describe("workspace members and roles", () => {
    it("reads a legacy or unknown role as member", async () => {
      stubFetchJson([
        { id: "m1", workspace_id: "ws-1", user_id: "u1", role: "owner", name: "Kun" },
        { id: "m2", workspace_id: "ws-1", user_id: "u2", role: "admin", name: "Ada" },
        { id: "m3", workspace_id: "ws-1", user_id: "u3", role: 42 },
        { id: "m4", workspace_id: "ws-1", user_id: "u4" },
      ]);
      const client = new ApiClient("https://api.example.test");
      const members = await client.listMembers("ws-1");
      expect(members.map((m) => m.role)).toEqual(["owner", "member", "member", "member"]);
    });

    it("drops malformed member rows and survives a non-array body", async () => {
      stubFetchJson([{ id: "m1", workspace_id: "ws-1", user_id: "u1", role: "member" }, { nope: true }]);
      const client = new ApiClient("https://api.example.test");
      await expect(client.listMembers("ws-1")).resolves.toHaveLength(1);

      stubFetchJson({ members: "wrong" });
      await expect(client.listMembers("ws-1")).resolves.toEqual([]);
    });

    it("reads a legacy share-link role as member", async () => {
      stubFetchJson({ workspace_name: "Office", workspace_slug: "office", role: "admin" });
      const client = new ApiClient("https://api.example.test");
      await expect(client.getShareLinkInfo("code")).resolves.toMatchObject({ role: "member" });
    });
  });

  describe("project members", () => {
    it("parses the shared-with list and drops malformed rows", async () => {
      stubFetchJson({
        members: [
          { user_id: "u1", created_at: "2026-10-13T00:00:00Z" },
          { user_id: 7 },
          { user_id: "u2" },
        ],
      });
      const client = new ApiClient("https://api.example.test");
      await expect(client.listProjectMembers("p1")).resolves.toEqual({
        members: [
          { user_id: "u1", created_at: "2026-10-13T00:00:00Z" },
          { user_id: "u2", created_at: "" },
        ],
      });
    });

    it("falls back to an empty list when the body is malformed", async () => {
      stubFetchJson({ members: null });
      const client = new ApiClient("https://api.example.test");
      await expect(client.listProjectMembers("p1")).resolves.toEqual({ members: [] });

      stubFetchJson(["not", "an", "object"]);
      await expect(client.listProjectMembers("p1")).resolves.toEqual({ members: [] });
    });

    it("falls back to the requested user when the add reply is malformed", async () => {
      stubFetchJson({ unexpected: true }, 201);
      const client = new ApiClient("https://api.example.test");
      await expect(client.addProjectMember("p1", "u9")).resolves.toEqual({
        user_id: "u9",
        created_at: "",
      });
    });
  });

  describe("listTimeline", () => {
    it("falls back to an empty array when the body is null", async () => {
      stubFetchJson(null);
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      expect(entries).toEqual([]);
    });

    it("falls back when the body is not an array", async () => {
      stubFetchJson({ wrong: "shape" });
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      expect(entries).toEqual([]);
    });

    it("accepts a new entry type rather than crashing on enum drift", async () => {
      stubFetchJson([
        {
          type: "future_kind", // not in TS union
          id: "e-1",
          actor_type: "member",
          actor_id: "u-1",
          created_at: "2026-01-01T00:00:00Z",
        },
      ]);
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      expect(entries).toHaveLength(1);
      expect(entries[0]?.type).toBe("future_kind");
    });

    // Forward-compat: when the server adds a new field to an existing
    // shape, `.loose()` lets it pass through unchanged. Without `.loose()`
    // zod 4 strips it, which would silently break a future TS type that
    // adopts the field — see schemas.ts header comment.
    it("preserves unknown fields the schema didn't list", async () => {
      stubFetchJson([
        {
          type: "comment",
          id: "e-1",
          actor_type: "member",
          actor_id: "u-1",
          created_at: "2026-01-01T00:00:00Z",
          // New server-side field not present in TimelineEntrySchema:
          future_field: { nested: "value" },
        },
      ]);
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      const entry = entries[0] as unknown as Record<string, unknown>;
      expect(entry.future_field).toEqual({ nested: "value" });
    });
  });

  describe("listIssues", () => {
    it("falls back to an empty list when the response is malformed", async () => {
      // `issues` having the wrong type triggers the fallback. An object
      // with only unexpected keys would *succeed* parsing now (every
      // declared field has a default) and just pass the extras through
      // via `.loose()`, so we use a wrong-type payload here instead.
      stubFetchJson({ issues: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listIssues();
      expect(res).toEqual({ issues: [], total: 0 });
    });
  });

  describe("getIssue", () => {
    // Unlike a list, a single issue has no safe-empty shape, and the bare
    // identifier autolink caches this result for 5 minutes. A malformed 2xx
    // must therefore fail the call instead of becoming a truthy issue with an
    // `undefined` id — and it must fail with something OTHER than an
    // ApiError 404, which `issueIdentifierOptions` maps to "no such issue".
    const validIssue = {
      id: "issue-1",
      workspace_id: "ws-1",
      number: 1,
      identifier: "MUL-1",
      title: "Existing",
      description: null,
      status: "todo",
      priority: "none",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 0,
      start_date: null,
      due_date: null,
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    };

    it("resolves a well-formed issue, defaulting the fields older servers omit", async () => {
      stubFetchJson({ ...validIssue, unknown_field: "kept" });
      const client = new ApiClient("https://api.example.test");
      const issue = await client.getIssue("MUL-1");
      expect(issue.id).toBe("issue-1");
      expect(issue.stage).toBeNull();
      expect(issue.metadata).toEqual({});
      expect(issue.properties).toEqual({});
    });

    it("rejects a 200 body that is not a usable issue (no truthy issue with an undefined id)", async () => {
      stubFetchJson({ not: "an issue" });
      const client = new ApiClient("https://api.example.test");
      await expect(client.getIssue("MUL-1")).rejects.toThrow();
    });

    it("rejects a 200 body whose required field drifted type", async () => {
      stubFetchJson({ ...validIssue, number: "1" });
      const client = new ApiClient("https://api.example.test");
      await expect(client.getIssue("MUL-1")).rejects.toThrow();
    });

    it("does not disguise a malformed body as a 404", async () => {
      stubFetchJson({ id: "issue-1" });
      const client = new ApiClient("https://api.example.test");
      await expect(client.getIssue("MUL-1")).rejects.not.toBeInstanceOf(
        ApiError,
      );
    });

    it("still surfaces a real 404 as an ApiError so autolink can resolve to null", async () => {
      stubFetchJson({ error: "issue not found" }, 404);
      const client = new ApiClient("https://api.example.test");
      await expect(client.getIssue("TES-1")).rejects.toMatchObject({
        status: 404,
      });
    });
  });

  describe("createIssue", () => {
    // The create modal decides whether to run its label-attach fallback by
    // reading `labels` off the parsed response, and treats a rejection as a
    // failed create (keep the draft, failure toast). So: a valid issue with
    // any labels shape resolves (labels absent → undefined, valid → Label[],
    // malformed → undefined), but a body that isn't a usable issue rejects
    // rather than fabricating a blank "success".
    const validIssue = {
      id: "issue-1",
      workspace_id: "ws-1",
      number: 1,
      identifier: "MUL-1",
      title: "Created",
      description: null,
      status: "todo",
      priority: "none",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 0,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    };
    const label = {
      id: "label-1",
      workspace_id: "ws-1",
      name: "bug",
      color: "#ef4444",
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    };

    it("keeps labels undefined when the backend omits the field (older backend)", async () => {
      stubFetchJson(validIssue, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      expect(issue.id).toBe("issue-1");
      expect(issue.labels).toBeUndefined();
    });

    it("validates a well-formed labels array", async () => {
      stubFetchJson({ ...validIssue, labels: [label] }, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      expect(issue.labels?.map((l) => l.id)).toEqual(["label-1"]);
    });

    it("degrades a null labels field to undefined so the client falls back", async () => {
      stubFetchJson({ ...validIssue, labels: null }, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      // The issue itself still parses; only the malformed labels degrade.
      expect(issue.id).toBe("issue-1");
      expect(issue.labels).toBeUndefined();
    });

    it("degrades a labels array of the wrong element shape to undefined", async () => {
      stubFetchJson({ ...validIssue, labels: [{ nope: true }] }, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      expect(issue.id).toBe("issue-1");
      expect(issue.labels).toBeUndefined();
    });

    it("rejects when the whole response body is not a usable issue (no fake success)", async () => {
      stubFetchJson({ not: "an issue" }, 201);
      const client = new ApiClient("https://api.example.test");
      await expect(client.createIssue({ title: "Created" })).rejects.toThrow();
    });

    it("rejects when the created issue has an empty id", async () => {
      stubFetchJson({ ...validIssue, id: "" }, 201);
      const client = new ApiClient("https://api.example.test");
      await expect(client.createIssue({ title: "Created" })).rejects.toThrow();
    });

    it("fails closed before POST when create properties are unsupported", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({}), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);
      const client = new ApiClient("https://api.example.test");

      await expect(
        client.createIssue({ title: "Created", properties: { "property-1": "value" } }),
      ).rejects.toThrow("does not support atomic custom properties");
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0]?.[0]).toBe("https://api.example.test/api/config");
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    });

    it("preflights properties and requires the canonical response snapshot", async () => {
      const fetchMock = vi
        .fn()
        .mockResolvedValueOnce(
          new Response(JSON.stringify({ issue_create_properties_supported: true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        )
        .mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              ...validIssue,
              properties: { "property-1": ["first", "second"] },
            }),
            { status: 201, headers: { "Content-Type": "application/json" } },
          ),
        );
      vi.stubGlobal("fetch", fetchMock);
      const client = new ApiClient("https://api.example.test");

      await expect(
        client.createIssue({
          title: "Created",
          properties: { "property-1": ["second", "first", "second"] },
        }),
      ).resolves.toMatchObject({ properties: { "property-1": ["first", "second"] } });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({ method: "POST" });
    });

    it("reports a created identifier when the response property snapshot mismatches", async () => {
      const fetchMock = vi
        .fn()
        .mockResolvedValueOnce(
          new Response(JSON.stringify({ issue_create_properties_supported: true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        )
        .mockResolvedValueOnce(
          new Response(JSON.stringify(validIssue), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        );
      vi.stubGlobal("fetch", fetchMock);
      const client = new ApiClient("https://api.example.test");

      await expect(
        client.createIssue({ title: "Created", properties: { "property-1": "value" } }),
      ).rejects.toThrow("Issue MUL-1 was created");
    });
  });

  describe("comment source-context sub-issues", () => {
    it("preflights manual sub-issue properties and validates their snapshot", async () => {
      const issue = {
        id: "issue-2",
        workspace_id: "ws-1",
        number: 2,
        identifier: "MUL-2",
        title: "Child",
        description: null,
        status: "todo",
        priority: "none",
        assignee_type: null,
        assignee_id: null,
        creator_type: "member",
        creator_id: "user-1",
        parent_issue_id: "issue-1",
        project_id: null,
        position: 0,
        stage: null,
        start_date: null,
        due_date: null,
        metadata: {},
        properties: { "property-1": true },
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      };
      const fetchMock = vi
        .fn()
        .mockResolvedValueOnce(
          new Response(JSON.stringify({ issue_create_properties_supported: true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        )
        .mockResolvedValueOnce(
          new Response(JSON.stringify(issue), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        );
      vi.stubGlobal("fetch", fetchMock);
      const client = new ApiClient("https://api.example.test");

      await expect(
        client.createCommentSubIssue("comment-1", {
          mode: "manual",
          capture_token: "token",
          issue: { title: "Child", properties: { "property-1": true } },
        }),
      ).resolves.toMatchObject({ id: "issue-2" });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(fetchMock.mock.calls[1]?.[0]).toBe(
        "https://api.example.test/api/comments/comment-1/sub-issues",
      );
    });

    it.each([404, 405])("maps server status %s to source_context_server_unsupported", async (status) => {
      stubFetchJson({ error: "not found" }, status);
      const client = new ApiClient("https://api.example.test");
      await expect(client.createCommentSubIssue("comment-1", {
        mode: "manual",
        capture_token: "token",
        issue: { title: "Child" },
      })).rejects.toMatchObject({ body: { code: "source_context_server_unsupported" } });
    });
  });

  describe("searchIssues", () => {
    it("falls back to an empty result when the response is malformed", async () => {
      stubFetchJson({ issues: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.searchIssues({ q: "bug" });
      expect(res).toEqual({ issues: [] });
    });

    it("accepts a response without an exact total", async () => {
      stubFetchJson({ issues: [] });
      const client = new ApiClient("https://api.example.test");
      const res = await client.searchIssues({ q: "bug" });
      expect(res).toEqual({ issues: [] });
    });
  });

  describe("searchProjects", () => {
    it("falls back to an empty result when the response is malformed", async () => {
      stubFetchJson({ projects: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.searchProjects({ q: "roadmap" });
      expect(res).toEqual({ projects: [] });
    });

    it("accepts a response without an exact total", async () => {
      stubFetchJson({ projects: [] });
      const client = new ApiClient("https://api.example.test");
      const res = await client.searchProjects({ q: "roadmap" });
      expect(res).toEqual({ projects: [] });
    });
  });

  // Local search index sync applies whatever these return to the local copy,
  // so a malformed body rejects instead of degrading to an empty page.
  describe("search index sync", () => {
    it("sends the workspace slug and parses a snapshot page", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            issues: [],
            comments: [{ id: "c1", issue_id: "i1", content: "hi", created_at: "2026-01-01T00:00:00.5Z" }],
            projects: [],
            next_after_number: 200,
            done: false,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
      vi.stubGlobal("fetch", fetchMock);
      const client = new ApiClient("https://api.example.test");

      const page = await client.getSearchIndexSnapshot({ workspaceSlug: "acme", afterNumber: 0, limit: 200 });

      expect(page.comments).toHaveLength(1);
      expect(page.done).toBe(false);
      const [url, init] = fetchMock.mock.calls[0]!;
      expect(String(url)).toBe("https://api.example.test/api/search-index/snapshot?after_number=0&limit=200");
      expect((init as RequestInit).headers).toMatchObject({ "X-Workspace-Slug": "acme" });
    });

    it("rejects malformed manifest, snapshot, and changes bodies", async () => {
      const client = new ApiClient("https://api.example.test");

      stubFetchJson({ cursor: "", issue_count: 0, comment_count: 0, project_count: 0, text_bytes: 0 });
      await expect(client.getSearchIndexManifest({ workspaceSlug: "acme" })).rejects.toThrow(/Malformed response/);

      stubFetchJson({ issues: "nope", comments: [], projects: [], next_after_number: 0, done: true });
      await expect(client.getSearchIndexSnapshot({ workspaceSlug: "acme", afterNumber: 0 })).rejects.toThrow(
        /Malformed response/,
      );

      stubFetchJson({ issues: [], comments: [], projects: [], cursor: "c2", has_more: false });
      await expect(client.getSearchIndexChanges({ workspaceSlug: "acme", cursor: "c1" })).rejects.toThrow(
        /Malformed response/,
      );
    });

    it("surfaces an expired cursor as a 410 ApiError", async () => {
      stubFetchJson({ error: "search index cursor expired; rebuild the local index" }, 410);
      const client = new ApiClient("https://api.example.test");
      const err = await client.getSearchIndexChanges({ workspaceSlug: "acme", cursor: "c1" }).catch((e) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).status).toBe(410);
    });
  });

  describe("getConfig", () => {
    it("drops malformed daemon setup URLs instead of throwing", async () => {
      stubFetchJson({
        cdn_domain: "cdn.example.com",
        allow_signup: true,
        daemon_server_url: { wrong: "shape" },
        daemon_app_url: 123,
        workspace_creation_disabled: false,
        feature_flags: { plugins_v1: true },
      });
      const client = new ApiClient("https://api.example.test");
      const config = await client.getConfig();
      expect(config.cdn_domain).toBe("cdn.example.com");
      expect(config.allow_signup).toBe(true);
      expect(config.daemon_server_url).toBeUndefined();
      expect(config.daemon_app_url).toBeUndefined();
      expect(config.feature_flags?.plugins_v1).toBe(true);
    });
  });

  describe("listGroupedIssues", () => {
    it("falls back to empty groups when the response is malformed", async () => {
      stubFetchJson({ groups: "not-an-array" });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listGroupedIssues({ group_by: "assignee" });
      expect(res).toEqual({ groups: [] });
    });
  });

  describe("listComments", () => {
    it("returns [] when the response is not an array", async () => {
      stubFetchJson({ wrong: "shape" });
      const client = new ApiClient("https://api.example.test");
      const comments = await client.listComments("issue-1");
      expect(comments).toEqual([]);
    });
  });

  describe("previewCommentTriggers", () => {
    it("returns an empty agent list when the response is malformed", async () => {
      stubFetchJson({ agents: "not-an-array" });
      const client = new ApiClient("https://api.example.test");
      const preview = await client.previewCommentTriggers("issue-1", "hello");
      expect(preview).toEqual({ agents: [] });
    });
  });

  describe("listIssueSubscribers", () => {
    it("returns [] when the response is null", async () => {
      stubFetchJson(null);
      const client = new ApiClient("https://api.example.test");
      const subs = await client.listIssueSubscribers("issue-1");
      expect(subs).toEqual([]);
    });
  });

  describe("listChildIssues", () => {
    it("returns { issues: [] } when the issues field is missing", async () => {
      stubFetchJson({});
      const client = new ApiClient("https://api.example.test");
      const res = await client.listChildIssues("issue-1");
      expect(res).toEqual({ issues: [] });
    });
  });

  describe("listIssueDuplicates", () => {

  });

  describe("getChildIssueProgress", () => {
    it("validates the response before query selectors iterate it", async () => {
      stubFetchJson({ progress: "invalid" });
      const client = new ApiClient("https://api.example.test");

      await expect(client.getChildIssueProgress()).resolves.toEqual({
        progress: [],
      });
    });
  });

});

// Direct tests for the helper, decoupled from any specific endpoint —
// guards against an endpoint refactor masking a regression in the helper.
describe("parseWithFallback", () => {
  const opts = { endpoint: "TEST /unit" };

  it("returns parsed data on success", () => {
    const schema = z.object({ id: z.string() });
    const out = parseWithFallback({ id: "x" }, schema, { id: "fallback" }, opts);
    expect(out).toEqual({ id: "x" });
  });

  it("returns the fallback when validation fails", () => {
    const schema = z.object({ id: z.string() });
    const fallback = { id: "fallback" };
    const out = parseWithFallback({ id: 123 }, schema, fallback, opts);
    expect(out).toBe(fallback);
  });

  it("returns the fallback when data is null", () => {
    const schema = z.object({ id: z.string() });
    const fallback = { id: "fallback" };
    const out = parseWithFallback(null, schema, fallback, opts);
    expect(out).toBe(fallback);
  });
});
