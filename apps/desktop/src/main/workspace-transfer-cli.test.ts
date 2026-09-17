// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  buildTransferCliArgs,
  classifyTransferError,
  parseTransferBindRuntimesReport,
  parseTransferEstimate,
  parseTransferImportReport,
  parseTransferProgressLine,
  parseTransferRunRequest,
  runTransferCli,
  type TransferCliDeps,
} from "./workspace-transfer-cli";

const PROFILE = "desktop-api.multica.ai";

describe("buildTransferCliArgs", () => {
  it("builds export args with profile, workspace, and out path", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "export",
      "--workspace",
      "acme",
      "--out",
      "/tmp/acme.zip",
    ]);
  });

  it("builds export --estimate without --out", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "export",
        workspace: "acme",
        estimate: true,
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "export",
      "--workspace",
      "acme",
      "--estimate",
    ]);
  });

  // The card's default export must stay runnable on a CLI that predates the
  // `issues` group, so the include flag is sent only for a deliberate tick
  // (contract §9.3).
  it("sends the issues include only when the task group was asked for", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
        includeIssues: true,
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "export",
      "--workspace",
      "acme",
      "--include",
      "config,conversations,attachments,issues",
      "--out",
      "/tmp/acme.zip",
    ]);
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
      }),
    ).not.toContain("--include");
    // The estimate has to measure the same package the export will write.
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "export",
        workspace: "acme",
        estimate: true,
        includeIssues: true,
      }),
    ).toContain("config,conversations,attachments,issues");
  });

  it("builds import args with --in and optional --dry-run", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "import",
      "--workspace",
      "acme",
      "--in",
      "/tmp/acme.zip",
      "--dry-run",
    ]);
  });

  it("refuses an empty profile", () => {
    expect(() =>
      buildTransferCliArgs("", {
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
      }),
    ).toThrow(/unresolved profile/);
  });

  // The card's defaults are the CLI's own defaults, so sending them would only
  // break a CLI that predates the flags (DENE-363). The issue prefix is the one
  // exception: its CLI default follows the bundle's task group (DENE-404), which
  // the card cannot see, so an unchecked box has to say so out loud.
  it("sends only the issue prefix for the default import switches", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        options: {
          activateAutopilots: true,
          applyWorkspaceSettings: true,
          applyIssuePrefix: true,
          autoBindRuntimes: true,
        },
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "import",
      "--workspace",
      "acme",
      "--in",
      "/tmp/acme.zip",
      "--apply-issue-prefix",
    ]);
  });

  it("spells out an unchecked issue prefix instead of relying on the CLI default", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        options: {
          activateAutopilots: true,
          applyWorkspaceSettings: true,
          applyIssuePrefix: false,
          autoBindRuntimes: true,
        },
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "import",
      "--workspace",
      "acme",
      "--in",
      "/tmp/acme.zip",
      "--apply-issue-prefix=false",
    ]);
  });

  it("sends only the option flags the user changed", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
        onConflict: "skip",
        options: {
          activateAutopilots: false,
          applyWorkspaceSettings: false,
          applyIssuePrefix: true,
          autoBindRuntimes: false,
        },
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "import",
      "--workspace",
      "acme",
      "--in",
      "/tmp/acme.zip",
      "--dry-run",
      "--on-conflict",
      "skip",
      "--activate-autopilots=false",
      "--apply-workspace-settings=false",
      "--apply-issue-prefix",
      "--auto-bind-runtimes=false",
    ]);
  });
});

describe("classifyTransferError", () => {
  it("maps target_unsupported / 404 copy", () => {
    expect(
      classifyTransferError(
        "target_unsupported: this server is not a kun instance with /transfer/* endpoints",
      ),
    ).toBe("target_unsupported");
  });

  it("maps a CLI that has no transfer subcommand", () => {
    expect(
      classifyTransferError('Error: unknown command "transfer" for "multica"'),
    ).toBe("cli_too_old");
  });

  it("maps a corrupt zip", () => {
    expect(
      classifyTransferError("transfer_bundle_corrupt: sha256 mismatch for manifest.json"),
    ).toBe("transfer_bundle_corrupt");
  });

  // The one task-import refusal a user can act on, so it gets its own code
  // instead of falling through to the raw 400 (DENE-387).
  it("maps the non-empty target refusal", () => {
    expect(
      classifyTransferError(
        'API error: 400 {"error":{"code":"transfer_issues_target_not_empty","message":"target workspace already has issues outside the bundle"}}',
      ),
    ).toBe("issues_target_not_empty");
  });
});

// DENE-364: the card applies the runtimes the auto-bind rule left alone through
// the CLI, so the args and the parser are the only path those picks travel.
describe("bind-runtimes", () => {
  it("builds one --bind flag per pick", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "bind-runtimes",
        workspace: "acme",
        bindings: [
          { agentId: "a1", runtimeId: "r1" },
          { agentId: "a2", runtimeId: "r2" },
        ],
      }),
    ).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "bind-runtimes",
      "--workspace",
      "acme",
      "--bind",
      "a1=r1",
      "--bind",
      "a2=r2",
    ]);
  });

  it("accepts a well-formed batch and refuses an empty or malformed one", () => {
    expect(
      parseTransferRunRequest({
        action: "bind-runtimes",
        workspace: "acme",
        bindings: [{ agentId: "a1", runtimeId: "r1" }],
      }),
    ).toEqual({
      action: "bind-runtimes",
      workspace: "acme",
      bindings: [{ agentId: "a1", runtimeId: "r1" }],
    });
    expect(
      parseTransferRunRequest({ action: "bind-runtimes", workspace: "acme", bindings: [] }),
    ).toBeNull();
    expect(
      parseTransferRunRequest({
        action: "bind-runtimes",
        workspace: "acme",
        bindings: [{ agentId: "a1" }],
      }),
    ).toBeNull();
  });

  it("reports one line per binding", () => {
    const report = parseTransferBindRuntimesReport(`{
  "applied": true,
  "bound": 1,
  "failed": 1,
  "bindings": [
    { "agent_id": "a1", "runtime_id": "r1", "agent_name": "Builder", "runtime_name": "kunkun-mbp", "bound": true },
    { "agent_id": "a2", "runtime_id": "r9", "agent_name": "Reviewer", "bound": false, "error_code": "runtime_not_found", "error": "runtime not found in this workspace" }
  ]
}`);
    expect(report.bound).toBe(1);
    expect(report.failed).toBe(1);
    expect(report.bindings[0]).toMatchObject({ agent_id: "a1", bound: true, runtime_name: "kunkun-mbp" });
    expect(report.bindings[1]).toMatchObject({ agent_id: "a2", bound: false, error_code: "runtime_not_found" });
  });
});

describe("parseTransferImportReport", () => {
  it("reads V2 config_report plus runtimes and gaps", () => {
    const report = parseTransferImportReport(`{
  "config_report": {
    "stats": { "created": 2, "updated": 1, "renamed": 0, "skipped": 0, "failed": 0 },
    "secrets_to_fill": [
      { "entity": "agent", "name": "Builder", "field": "custom_env", "target_id": "a1" }
    ]
  },
  "runtimes_to_bind": [
    { "agent_name": "Builder", "provider": "claude", "runtime_mode": "local", "profile_name": "default", "agent_target_id": "a1", "candidate_ids": ["r1"] }
  ],
  "export_gaps": [
    { "group": "plugins", "reason": "read_api_missing", "status": 404 }
  ]
}`);
    expect(report.stats.created).toBe(2);
    expect(report.secrets_to_fill[0]).toMatchObject({
      entity: "agent",
      name: "Builder",
      field: "custom_env",
    });
    expect(report.runtimes_to_bind[0]?.agent_name).toBe("Builder");
    // A server predating the three-tier rule sends no status and no candidate
    // objects; the row must still render as something to act on (DENE-364).
    expect(report.runtimes_to_bind[0]?.status).toBe("pending");
    expect(report.runtimes_to_bind[0]?.candidates[0]?.id).toBe("r1");
    expect(report.export_gaps[0]).toMatchObject({
      group: "plugins",
      reason: "read_api_missing",
    });
  });

  // "The automations didn't migrate" was really "they all arrived paused"; the
  // card can only explain that if the report says how many arrived (DENE-363).
  it("counts the automations an import wrote, ignoring the skipped rows", () => {
    const report = parseTransferImportReport(`{
  "config_report": {
    "stats": { "created": 3, "updated": 0, "renamed": 0, "skipped": 1, "failed": 0 },
    "batches": [
      { "entity_type": "agents", "batch_status": "committed", "items": [{ "action": "created" }] },
      { "entity_type": "autopilots", "batch_status": "committed", "items": [
        { "action": "created" },
        { "action": "updated" },
        { "action": "skipped" }
      ] }
    ],
    "warnings": [{ "code": "autopilots_imported_paused", "count": 3 }]
  }
}`);
    expect(report.autopilots).toEqual({ imported: 2 });
  });

  it("reports no automations when the bundle carried none", () => {
    const report = parseTransferImportReport(`{"config_report":{"stats":{}}}`);
    expect(report.autopilots).toEqual({ imported: 0 });
  });

  // V3: the report answers "did my tasks arrive, and what lost a reference?".
  // The rows themselves are one array per kind, which is unreadable in a card,
  // so the parser flattens them to counts (contract §9.6).
  it("reads the task counts and flattens the degraded rows", () => {
    const report = parseTransferImportReport(`{
  "config_report": { "stats": { "created": 2 } },
  "issues_report": {
    "applied": true,
    "issues_created": 42,
    "issues_skipped": 1,
    "comments_created": 118,
    "comments_skipped": 3,
    "labels_created": 5,
    "reactions_created": 4,
    "subscribers_created": 90,
    "parents_backfilled": 12,
    "status_unmapped": [{ "entity": "issue" }, { "entity": "issue" }],
    "assignee_unmapped": [{ "entity": "issue" }],
    "mention_unmapped": [{ "entity": "comment" }]
  }
}`);
    expect(report.issues).toEqual({
      applied: true,
      issuesCreated: 42,
      issuesSkipped: 1,
      commentsCreated: 118,
      commentsSkipped: 3,
      labelsCreated: 5,
      reactionsCreated: 4,
      subscribersCreated: 90,
      parentsBackfilled: 12,
      degraded: [
        { kind: "status", count: 2 },
        { kind: "assignee", count: 1 },
        { kind: "mention", count: 1 },
      ],
    });
  });

  it("leaves the task section absent when the bundle carried no tasks", () => {
    expect(
      parseTransferImportReport(`{"config_report":{"stats":{}}}`).issues,
    ).toBeUndefined();
  });
});

describe("parseTransferEstimate / progress", () => {
  it("parses estimate JSON", () => {
    expect(
      parseTransferEstimate(
        '{"sessions":12,"messages":80,"attachments":5,"attachment_bodies":3,"estimated_bytes":4096}',
      ),
    ).toEqual({
      sessions: 12,
      messages: 80,
      attachments: 5,
      attachment_bodies: 3,
      estimated_bytes: 4096,
    });
  });

  it("parses a structured progress line and ignores raw logs", () => {
    expect(parseTransferProgressLine("Export complete.")).toBeNull();
    expect(
      parseTransferProgressLine(
        '{"event":"progress","sessions_total":12,"session_index":3,"session_title":"Deploy","attachments_downloaded":4}',
      ),
    ).toMatchObject({
      phase: "running",
      sessionsTotal: 12,
      sessionsDone: 3,
      currentSessionTitle: "Deploy",
      attachmentsDownloaded: 4,
    });
  });

  // Imports upload attachments instead of downloading them, and the card has
  // one attachment counter, so the parser accepts both spellings (DENE-318).
  it("reads the import direction's uploaded counter", () => {
    expect(
      parseTransferProgressLine(
        '{"event":"progress","attachments_uploaded":3,"attachments_total":29}',
      ),
    ).toMatchObject({
      phase: "running",
      attachmentsDownloaded: 3,
      attachmentsTotal: 29,
    });
  });

  it("leaves counters absent when the line omits them", () => {
    const event = parseTransferProgressLine(
      '{"event":"progress","sessions_total":2}',
    );
    expect(event?.sessionsTotal).toBe(2);
    expect(event?.attachmentsTotal).toBeUndefined();
  });
});

describe("parseTransferRunRequest", () => {
  it("rejects relative paths", () => {
    expect(
      parseTransferRunRequest({
        action: "export",
        workspace: "acme",
        outPath: "acme.zip",
      }),
    ).toBeNull();
  });

  it("accepts an absolute export path", () => {
    expect(
      parseTransferRunRequest({
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
      }),
    ).toEqual({
      action: "export",
      workspace: "acme",
      outPath: "/tmp/acme.zip",
    });
  });

  it("carries the task-group tick and drops it when it is absent", () => {
    expect(
      parseTransferRunRequest({
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
        includeIssues: true,
      }),
    ).toEqual({
      action: "export",
      workspace: "acme",
      outPath: "/tmp/acme.zip",
      includeIssues: true,
    });
    expect(
      parseTransferRunRequest({
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
        includeIssues: "yes",
      }),
    ).toEqual({
      action: "export",
      workspace: "acme",
      outPath: "/tmp/acme.zip",
    });
  });

  // A renderer older than the switch sends no `options` field at all, and it
  // still has to get the migration's defaults — including the issue prefix,
  // without which the imported `<PREFIX>-xxx` references point at nothing
  // (DENE-404).
  it("carries the import option switches, defaulting the ones it omits", () => {
    expect(
      parseTransferRunRequest({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
        options: { autoBindRuntimes: false },
      }),
    ).toEqual({
      action: "import",
      workspace: "acme",
      inPath: "/tmp/acme.zip",
      dryRun: true,
      options: {
        activateAutopilots: true,
        applyWorkspaceSettings: true,
        applyIssuePrefix: true,
        autoBindRuntimes: false,
      },
    });
  });

  it("ignores a malformed options payload instead of failing the import", () => {
    expect(
      parseTransferRunRequest({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: false,
        options: "activate",
      }),
    ).toEqual({
      action: "import",
      workspace: "acme",
      inPath: "/tmp/acme.zip",
      dryRun: false,
    });
  });
});

describe("runTransferCli", () => {
  it("runs estimate then export with the Desktop profile", async () => {
    const calls: string[][] = [];
    const deps = mockDeps({
      runCommand: async (_bin, args) => {
        calls.push(args);
        if (args.includes("--estimate")) {
          return {
            code: 0,
            stdout:
              '{"sessions":4,"messages":10,"attachments":1,"attachment_bodies":1,"estimated_bytes":100}',
            stderr: "",
          };
        }
        return { code: 0, stdout: "/tmp/acme.zip\n", stderr: "" };
      },
    });

    const result = await runTransferCli(
      { action: "export", workspace: "acme", outPath: "/tmp/acme.zip" },
      deps,
    );

    expect(result).toEqual({
      ok: true,
      action: "export",
      outPath: "/tmp/acme.zip",
      bytes: 42,
    });
    expect(calls[0]).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "export",
      "--workspace",
      "acme",
      "--estimate",
    ]);
    expect(calls[1]).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "export",
      "--workspace",
      "acme",
      "--out",
      "/tmp/acme.zip",
    ]);
  });

  it("runs import --dry-run then returns the parsed report", async () => {
    const calls: string[][] = [];
    const deps = mockDeps({
      runCommand: async (_bin, args) => {
        calls.push(args);
        return {
          code: 0,
          stdout: JSON.stringify({
            config_report: {
              stats: { created: 1, updated: 0, renamed: 0, skipped: 0, failed: 0 },
              secrets_to_fill: [
                { entity: "agent", name: "Builder", field: "custom_env", target_id: "a1" },
              ],
            },
            runtimes_to_bind: [{ agent_name: "Builder", provider: "claude" }],
            export_gaps: [{ group: "skills", reason: "read_api_error", status: 500 }],
          }),
          stderr: "",
        };
      },
    });

    const result = await runTransferCli(
      {
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
        options: {
          activateAutopilots: false,
          applyWorkspaceSettings: true,
          applyIssuePrefix: false,
          autoBindRuntimes: true,
        },
      },
      deps,
    );

    expect(calls[0]).toEqual([
      "--profile",
      PROFILE,
      "transfer",
      "import",
      "--workspace",
      "acme",
      "--in",
      "/tmp/acme.zip",
      "--dry-run",
      "--activate-autopilots=false",
      "--apply-issue-prefix=false",
    ]);
    expect(result.ok).toBe(true);
    if (result.ok && result.action === "import") {
      expect(result.dryRun).toBe(true);
      expect(result.report.secrets_to_fill).toHaveLength(1);
      expect(result.report.runtimes_to_bind[0]?.agent_name).toBe("Builder");
      expect(result.report.export_gaps[0]?.group).toBe("skills");
    }
  });

  it("classifies a missing transfer subcommand as cli_too_old", async () => {
    const deps = mockDeps({
      runCommand: async () => ({
        code: 1,
        stdout: "",
        stderr: 'Error: unknown command "transfer" for "multica"',
      }),
    });
    const result = await runTransferCli(
      { action: "export", workspace: "acme", outPath: "/tmp/acme.zip" },
      deps,
    );
    expect(result).toMatchObject({ ok: false, code: "cli_too_old" });
  });
});

function mockDeps(overrides: Partial<TransferCliDeps> = {}): TransferCliDeps {
  return {
    resolveCli: async () => "/usr/local/bin/multica",
    profileName: async () => PROFILE,
    runCommand: async () => ({ code: 0, stdout: "", stderr: "" }),
    statSize: async () => 42,
    sendProgress: vi.fn(),
    ...overrides,
  };
}

// An export emits a progress line per session and per attachment, all on the
// same stderr the failure message is read from. Keeping them would push the
// CLI's actual error past the message cap and show the user a wall of JSON
// instead of the reason (DENE-318).
describe("transfer failure messages", () => {
  it("shows the CLI error, not the progress lines that preceded it", async () => {
    const progress = Array.from({ length: 26 }, (_, i) =>
      JSON.stringify({
        event: "progress",
        session_index: i + 1,
        sessions_total: 26,
        session_title: `Session number ${i + 1}`,
        attachments_downloaded: i + 1,
      }),
    );
    const stderr = `${progress.join("\n")}\nError: entity already exists: Backlog\n`;
    const result = await runTransferCli(
      { action: "export", workspace: "acme", outPath: "/tmp/acme.zip" },
      {
        resolveCli: async () => "/bin/multica",
        profileName: async () => "profile",
        sendProgress: () => {},
        statSize: async () => 0,
        runCommand: async () => ({ code: 1, stdout: "", stderr }),
      },
    );
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.message).toBe("Error: entity already exists: Backlog");
  });
});
