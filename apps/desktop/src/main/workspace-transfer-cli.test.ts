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

  it("refuses an empty profile", () => {
    expect(() =>
      buildTransferCliArgs("", {
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
      }),
    ).toThrow(/unresolved profile/);
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
    expect(report.export_gaps[0]).toMatchObject({
      group: "plugins",
      reason: "read_api_missing",
    });
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
