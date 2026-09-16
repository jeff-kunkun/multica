// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "./client";
import {
  CONFIG_BUNDLE_FORMAT,
  CONFIG_BUNDLE_SCHEMA_VERSION,
  EMPTY_CONFIG_BUNDLE,
  EMPTY_CONFIG_IMPORT_REPORT,
  configExportFilename,
  parseConfigBundle,
  parseLocalConfigBundle,
  parseTransferBindRuntimesReport,
} from "./config-transfer";

function stubFetchJson(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

const validBundle = {
  format: CONFIG_BUNDLE_FORMAT,
  schema_version: CONFIG_BUNDLE_SCHEMA_VERSION,
  bundle_id: "bundle-1",
  exported_at: "2026-09-15T00:00:00Z",
  source: {
    workspace_id: "ws-src",
    slug: "acme",
    name: "Acme",
    issue_prefix: "ACM",
    exported_by: "user-1",
  },
  entities: { labels: [{ source_id: "l1", name: "bug" }] },
  secrets_omitted: [
    {
      entity: "agent",
      source_id: "a1",
      name: "Builder",
      field: "custom_env",
      reason: "secret_material",
    },
  ],
  stats: { labels: 1 },
};

const validReport = {
  applied: false,
  bundle_id: "bundle-1",
  on_conflict: "skip",
  batches: [
    {
      entity_type: "labels",
      batch_status: "preview",
      items: [
        { source_id: "l1", name: "bug", action: "skipped", reason: "exists" },
      ],
    },
  ],
  unmapped_refs: [],
  secrets_to_fill: [
    { entity: "agent", name: "Builder", field: "custom_env", path: "/acme/agents/a1" },
  ],
  warnings: [{ code: "autopilots_imported_paused", count: 1 }],
  stats: { created: 0, updated: 0, renamed: 0, skipped: 1, failed: 0 },
};

describe("parseLocalConfigBundle", () => {
  it("accepts a v1 workspace-config bundle", () => {
    const result = parseLocalConfigBundle(validBundle);
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.bundle.entities).toEqual(validBundle.entities);
      expect(result.bundle.secrets_omitted[0]?.field).toBe("custom_env");
    }
  });

  it("rejects a missing format", () => {
    expect(parseLocalConfigBundle({ schema_version: 1 }).ok).toBe(false);
  });

  it("rejects an unsupported schema version", () => {
    const result = parseLocalConfigBundle({
      ...validBundle,
      schema_version: 99,
    });
    expect(result).toEqual({
      ok: false,
      code: "config_bundle_version_unsupported",
    });
  });
});

describe("configExportFilename", () => {
  it("includes slug and UTC date", () => {
    expect(configExportFilename("acme", new Date("2026-09-15T12:00:00Z"))).toBe(
      "multica-config-acme-20260915.json",
    );
  });
});

describe("exportWorkspaceConfig schema fallback", () => {
  it("returns the empty bundle when the export body is malformed", async () => {
    stubFetchJson({ not: "a bundle" });
    const client = new ApiClient("https://api.example.test");
    await expect(client.exportWorkspaceConfig("ws-1")).resolves.toEqual(
      EMPTY_CONFIG_BUNDLE,
    );
  });

  it("keeps entities when the export body is well-formed", async () => {
    stubFetchJson(validBundle);
    const client = new ApiClient("https://api.example.test");
    const bundle = await client.exportWorkspaceConfig("ws-1");
    expect(bundle.format).toBe(CONFIG_BUNDLE_FORMAT);
    expect(bundle.entities).toEqual(validBundle.entities);
  });
});

describe("importWorkspaceConfig schema fallback", () => {
  it("returns the empty report when a 200 body is malformed", async () => {
    stubFetchJson({ nope: true });
    const client = new ApiClient("https://api.example.test");
    const result = await client.importWorkspaceConfig("ws-1", {
      bundle: validBundle,
      dry_run: true,
    });
    expect(result.report).toEqual(EMPTY_CONFIG_IMPORT_REPORT);
    expect(result.error).toBeUndefined();
  });

  it("parses a conflict report from a 409 body instead of throwing away the preview", async () => {
    stubFetchJson(
      {
        error: "import conflict",
        code: "config_import_conflict",
        report: validReport,
      },
      409,
    );
    const client = new ApiClient("https://api.example.test");
    const result = await client.importWorkspaceConfig("ws-1", {
      bundle: validBundle,
      dry_run: true,
      on_conflict: "fail",
    });
    expect(result.error).toEqual({
      code: "config_import_conflict",
      message: "import conflict",
      status: 409,
    });
    expect(result.report.stats.skipped).toBe(1);
    expect(result.report.batches[0]?.items[0]?.name).toBe("bug");
  });

  it("still throws when a 4xx has no report", async () => {
    stubFetchJson(
      { error: "invalid request body", code: "config_bundle_invalid" },
      400,
    );
    const client = new ApiClient("https://api.example.test");
    await expect(
      client.importWorkspaceConfig("ws-1", { bundle: {}, dry_run: true }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

describe("parseConfigBundle", () => {
  it("does not throw on null", () => {
    expect(parseConfigBundle(null, "test")).toEqual(EMPTY_CONFIG_BUNDLE);
  });
});

describe("bindTransferRuntimes schema fallback", () => {
  it("posts the bindings and reads the per-row report", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          applied: true,
          bound: 1,
          failed: 1,
          results: [
            {
              agent_id: "a1",
              agent_name: "Builder",
              runtime_id: "r1",
              runtime_name: "Claude (mac)",
              ok: true,
            },
            {
              agent_id: "a2",
              runtime_id: "r9",
              ok: false,
              reason_code: "runtime_private",
              reason: "this runtime is not available to you",
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const report = await client.bindTransferRuntimes("ws-1", [
      { agent_id: "a1", runtime_id: "r1" },
      { agent_id: "a2", runtime_id: "r9" },
    ]);

    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/workspaces/ws-1/transfer/bind-runtimes",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          bindings: [
            { agent_id: "a1", runtime_id: "r1" },
            { agent_id: "a2", runtime_id: "r9" },
          ],
        }),
      }),
    );
    expect(report.bound).toBe(1);
    expect(report.results[0]).toMatchObject({ ok: true, runtime_name: "Claude (mac)" });
    expect(report.results[1]).toMatchObject({
      ok: false,
      reason_code: "runtime_private",
    });
  });

  it("falls back to an empty report on a malformed body", () => {
    // Per-row results are what the card renders; a body that does not match
    // must degrade to "nothing landed" rather than throw past the caller.
    const report = parseTransferBindRuntimesReport(
      { bound: "many", results: { not: "an array" } },
      "test",
    );
    expect(report).toEqual({ applied: false, bound: 0, failed: 0, results: [] });
  });
});
