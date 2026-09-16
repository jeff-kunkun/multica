// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  buildTransferCliArgs,
  classifyTransferError,
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

  it("omits --auto-bind-runtimes when the toggle is on and sends =false when off", () => {
    const base = {
      action: "import" as const,
      workspace: "acme",
      inPath: "/tmp/acme.zip",
    };
    // The CLI default is on, so the enabled case stays flag-free: an older
    // CLI would reject an unknown flag (DENE-364).
    expect(buildTransferCliArgs(PROFILE, { ...base, autoBindRuntimes: true })).not.toContain(
      "--auto-bind-runtimes=false",
    );
    expect(buildTransferCliArgs(PROFILE, { ...base, autoBindRuntimes: false })).toContain(
      "--auto-bind-runtimes=false",
    );
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
  // break a CLI that predates the flags (DENE-363).
  it("sends no option flag for the default import switches", () => {
    expect(
      buildTransferCliArgs(PROFILE, {
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        options: {
          activateAutopilots: true,
          applyWorkspaceSettings: true,
          applyIssuePrefix: false,
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
    // A server that predates the bind report leaves the action empty and the
    // card falls back to the plain candidate list.
    expect(report.runtimes_to_bind[0]?.action).toBe("");
    expect(report.export_gaps[0]).toMatchObject({
      group: "plugins",
      reason: "read_api_missing",
    });
  });

  it("reads the bind outcome, candidates and reason", () => {
    const report = parseTransferImportReport(`{
  "runtimes_to_bind": [
    {
      "agent_target_id": "a1",
      "agent_name": "Builder",
      "provider": "claude",
      "runtime_mode": "local",
      "action": "candidates",
      "auto_bind": false,
      "candidate_ids": ["r1", "r2"],
      "candidates": [
        { "runtime_id": "r1", "name": "Claude (MacBook-Pro.local)", "provider": "claude", "runtime_mode": "local", "profile_name": "" },
        { "runtime_id": "r2", "name": "Claude (MacBook-Air-5.local)", "provider": "claude", "runtime_mode": "local", "profile_name": "corp" }
      ]
    },
    {
      "agent_target_id": "a2",
      "agent_name": "Reviewer",
      "action": "no_candidate",
      "reason_code": "no_runtime_for_provider",
      "reason": "no runtime matching provider=codex"
    },
    {
      "agent_target_id": "a3",
      "agent_name": "Ops",
      "action": "not_a_real_action",
      "candidates": [{ "runtime_id": "r3", "name": "Codex (mac)" }]
    }
  ]
}`);
    const pick = report.runtimes_to_bind[0]!;
    expect(pick.action).toBe("candidates");
    expect(pick.candidate_ids).toEqual(["r1", "r2"]);
    expect(pick.candidates).toHaveLength(2);
    expect(pick.candidates[1]).toMatchObject({
      runtime_id: "r2",
      name: "Claude (MacBook-Air-5.local)",
      profile_name: "corp",
    });
    expect(report.runtimes_to_bind[1]).toMatchObject({
      action: "no_candidate",
      reason_code: "no_runtime_for_provider",
    });
    // An unknown token degrades to "" so a newer server cannot silently drop
    // the row.
    expect(report.runtimes_to_bind[2]?.action).toBe("");
    expect(report.runtimes_to_bind[2]?.candidates).toEqual([
      {
        runtime_id: "r3",
        name: "Codex (mac)",
        provider: "",
        runtime_mode: "",
        profile_name: "",
      },
    ]);
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

  it("carries autoBindRuntimes=false through, and drops the flag when it is on", () => {
    const base = {
      action: "import",
      workspace: "acme",
      inPath: "/tmp/acme.zip",
      dryRun: false,
    };
    expect(parseTransferRunRequest({ ...base, autoBindRuntimes: false })).toEqual({
      ...base,
      autoBindRuntimes: false,
    });
    expect(parseTransferRunRequest({ ...base, autoBindRuntimes: true })).toEqual(base);
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

  it("carries the import option switches, defaulting the ones it omits", () => {
    expect(
      parseTransferRunRequest({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
        options: { applyIssuePrefix: true },
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
