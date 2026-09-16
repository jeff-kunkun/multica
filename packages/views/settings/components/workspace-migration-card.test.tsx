import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import type { TransferProgressEvent, TransferRunResult } from "../../platform";

const desktop = vi.hoisted(() => ({
  isDesktop: true,
  pickExport: vi.fn(),
  pickImport: vi.fn(),
  run: vi.fn(),
  subscribe: vi.fn<(cb: (event: unknown) => void) => () => void>(() => () => {}),
}));

vi.mock("../../platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../platform")>();
  return {
    ...actual,
    isDesktopShell: () => desktop.isDesktop,
    pickTransferExportPath: (slug?: string) => desktop.pickExport(slug),
    pickTransferImportPath: () => desktop.pickImport(),
    runWorkspaceTransfer: (request: unknown) => desktop.run(request),
    subscribeTransferProgress: (cb: (event: TransferProgressEvent) => void) =>
      desktop.subscribe(cb as (event: unknown) => void),
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => ({
  paths: (await importOriginal<typeof import("@multica/core/paths")>()).paths,
  useCurrentWorkspace: () => ({
    id: "ws-1",
    name: "Acme",
    slug: "acme",
  }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: "owner", isLoading: false }),
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      getBaseUrl: () => "https://api.multica.ai",
      exportWorkspaceConfig: vi.fn(),
      importWorkspaceConfig: vi.fn(),
    },
  };
});

vi.mock("../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { toast } from "sonner";
import { TRANSFER_EXPORT_COMPLETED_KEY } from "../../platform";
import { WorkspaceMigrationCard } from "./workspace-migration-card";
import { ConfigTransferTab } from "./config-transfer-tab";

const importReport = {
  secrets_to_fill: [
    { entity: "agent", name: "Builder", field: "custom_env", target_id: "a1" },
  ],
  runtimes_to_bind: [
    {
      agent_target_id: "a1",
      agent_name: "Builder",
      provider: "claude",
      runtime_mode: "local",
      profile_name: "default",
      status: "pending" as const,
      reason_code: "",
      reason: "",
      bound_runtime_id: "",
      bound_runtime_name: "",
      candidate_ids: ["r1"],
      candidates: [
        {
          id: "r1",
          name: "kunkun-mbp",
          provider: "claude",
          runtime_mode: "local",
          profile_name: "default",
        },
      ],
    },
  ],
  export_gaps: [{ group: "plugins", reason: "read_api_missing", status: 404 }],
  stats: { created: 3, updated: 1, renamed: 0, skipped: 0, failed: 0 },
  autopilots: { imported: 2 },
};

/**
 * The card's own defaults: a cross-environment import reproduces the
 * environment, so automations arrive running, the workspace settings land and a
 * single unambiguous runtime is bound (DENE-363 / DENE-364); the issue prefix
 * stays put because adopting it changes every later issue key.
 */
const DEFAULT_IMPORT_OPTIONS = {
  activateAutopilots: true,
  applyWorkspaceSettings: true,
  applyIssuePrefix: false,
  autoBindRuntimes: true,
};

function renderCard() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <WorkspaceMigrationCard />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  desktop.isDesktop = true;
  desktop.pickExport.mockReset();
  desktop.pickImport.mockReset();
  desktop.run.mockReset();
  desktop.subscribe.mockReset();
  desktop.subscribe.mockReturnValue(() => {});
  vi.mocked(toast.error).mockClear();
  vi.mocked(toast.success).mockClear();
  window.localStorage.removeItem(TRANSFER_EXPORT_COMPLETED_KEY);
});

describe("WorkspaceMigrationCard", () => {
  it("shows the current server host and workspace as the export source", () => {
    renderCard();
    const source = screen.getByTestId("workspace-migration-export-source");
    expect(source).toHaveTextContent("Will export from Acme on api.multica.ai.");
    expect(source).toHaveTextContent(
      "Export here first, then switch servers. After you switch to a self-hosted instance, the export source becomes that instance.",
    );
  });

  it("does not render outside the desktop shell", () => {
    desktop.isDesktop = false;
    renderCard();
    expect(screen.queryByTestId("workspace-migration-card")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Export to zip" }),
    ).not.toBeInTheDocument();
  });

  it("sends export with the workspace slug and chosen path", async () => {
    const user = userEvent.setup();
    desktop.pickExport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "export",
      outPath: "/tmp/acme.zip",
      bytes: 2048,
    } satisfies TransferRunResult);
    renderCard();

    await user.click(screen.getByRole("button", { name: "Export to zip" }));

    await waitFor(() =>
      expect(desktop.run).toHaveBeenCalledWith({
        action: "export",
        workspace: "acme",
        outPath: "/tmp/acme.zip",
      }),
    );
    expect(desktop.pickExport).toHaveBeenCalledWith("acme");
    expect(screen.getByText("Saved as acme.zip (2.0 KB)")).toBeInTheDocument();
    expect(window.localStorage.getItem(TRANSFER_EXPORT_COMPLETED_KEY)).toBe("1");
  });

  it("previews a dry-run report then applies after confirmation", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run
      .mockResolvedValueOnce({
        ok: true,
        action: "import",
        dryRun: true,
        report: importReport,
      })
      .mockResolvedValueOnce({
        ok: true,
        action: "import",
        dryRun: false,
        report: importReport,
      });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));

    expect(await screen.findByTestId("workspace-migration-report")).toBeInTheDocument();
    expect(screen.getByText("agent · Builder · custom_env")).toBeInTheDocument();
    // The runtime row names the agent and its source triple separately, so the
    // candidate picker underneath is unambiguous (DENE-364).
    expect(screen.getByText("claude · local · default")).toBeInTheDocument();
    expect(screen.getByText("plugins · read_api_missing")).toBeInTheDocument();
    expect(screen.getByText("New")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(desktop.run).toHaveBeenCalledWith({
      action: "import",
      workspace: "acme",
      inPath: "/tmp/acme.zip",
      dryRun: true,
      onConflict: "skip",
      options: DEFAULT_IMPORT_OPTIONS,
    });

    await user.click(
      screen.getByRole("button", { name: "Import into this workspace" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Import into this workspace" }),
    );

    await waitFor(() =>
      expect(desktop.run).toHaveBeenCalledWith({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: false,
        onConflict: "skip",
        options: DEFAULT_IMPORT_OPTIONS,
      }),
    );
  });

  // Every workspace ships issue statuses, labels and system agents, so an
  // import that defaults to "fail" 409s on the first dry-run and the user never
  // sees a preview at all (DENE-318).
  it("imports with skip by default and re-previews when the policy changes", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: true,
      report: importReport,
    });
    renderCard();

    expect(
      screen.getByRole("combobox", { name: "If something already exists" }),
    ).toHaveTextContent("Skip");

    await user.click(screen.getByRole("button", { name: "Import from zip" }));
    await waitFor(() =>
      expect(desktop.run).toHaveBeenLastCalledWith({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
        onConflict: "skip",
        options: DEFAULT_IMPORT_OPTIONS,
      }),
    );

    // Switching the policy refreshes the report the user is reading, and the
    // button must say so: a re-preview is not an apply (DENE-318).
    let finishPreview: (value: TransferRunResult) => void = () => {};
    desktop.run.mockImplementationOnce(
      () =>
        new Promise<TransferRunResult>((resolve) => {
          finishPreview = resolve;
        }),
    );
    await user.click(
      screen.getByRole("combobox", { name: "If something already exists" }),
    );
    await user.click(await screen.findByRole("option", { name: "Overwrite" }));
    expect(await screen.findByRole("button", { name: "Previewing…" })).toBeDisabled();
    await act(async () => {
      finishPreview({
        ok: true,
        action: "import",
        dryRun: true,
        report: importReport,
      });
    });
    await waitFor(() =>
      expect(desktop.run).toHaveBeenLastCalledWith({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: true,
        onConflict: "overwrite",
        options: DEFAULT_IMPORT_OPTIONS,
      }),
    );

    await user.click(
      screen.getByRole("button", { name: "Import into this workspace" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Import into this workspace" }),
    );
    await waitFor(() =>
      expect(desktop.run).toHaveBeenLastCalledWith({
        action: "import",
        workspace: "acme",
        inPath: "/tmp/acme.zip",
        dryRun: false,
        onConflict: "overwrite",
        options: DEFAULT_IMPORT_OPTIONS,
      }),
    );
  });

  // The V2 card used to drop the import switches the V1 path had, so every
  // migrated automation landed paused and the migration looked like it never
  // copied them (DENE-363).
  it("starts from the migration defaults and sends them with the run", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: true,
      report: importReport,
    });
    renderCard();

    expect(
      screen.getByRole("checkbox", { name: "Activate automations after import" }),
    ).toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: "Apply workspace settings" }),
    ).toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: "Adopt the issue prefix" }),
    ).not.toBeChecked();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));
    await waitFor(() =>
      expect(desktop.run).toHaveBeenLastCalledWith(
        expect.objectContaining({ options: DEFAULT_IMPORT_OPTIONS }),
      ),
    );
  });

  it("sends the switches the user changed", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: true,
      report: importReport,
    });
    renderCard();

    await user.click(
      screen.getByRole("checkbox", { name: "Activate automations after import" }),
    );
    await user.click(
      screen.getByRole("checkbox", { name: "Adopt the issue prefix" }),
    );
    await user.click(screen.getByRole("button", { name: "Import from zip" }));

    await waitFor(() =>
      expect(desktop.run).toHaveBeenLastCalledWith(
        expect.objectContaining({
          options: {
            activateAutopilots: false,
            applyWorkspaceSettings: true,
            applyIssuePrefix: true,
            autoBindRuntimes: true,
          },
        }),
      ),
    );
  });

  // "Automations: 2 imported, 2 of them paused." is the readable form of a
  // report a user otherwise reads as "the automations never came across",
  // followed by the one click that starts them (DENE-363).
  it("turns the paused automations into a readable line and a way out", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: true,
      report: importReport,
    });
    renderCard();

    // Turning activation off is what makes a run write them paused, so the
    // report has to say how many that is.
    await user.click(
      screen.getByRole("checkbox", { name: "Activate automations after import" }),
    );
    await user.click(screen.getByRole("button", { name: "Import from zip" }));

    const line = await screen.findByTestId("workspace-migration-autopilots");
    expect(line).toHaveTextContent("Automations: 2 imported, 2 of them paused.");
    expect(line).toHaveTextContent("start these straight away");

    // Turning activation back on is the way out, and the line follows it.
    await user.click(
      screen.getByRole("checkbox", { name: "Activate automations after import" }),
    );
    expect(
      screen.getByTestId("workspace-migration-autopilots"),
    ).toHaveTextContent(
      "Automations: 2 imported, triggering as soon as the import finishes.",
    );
    expect(
      screen.getByTestId("workspace-migration-autopilots"),
    ).not.toHaveTextContent("2 of them paused");
  });

  it("says automations are running when activation is on", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: true,
      report: importReport,
    });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));

    const line = await screen.findByTestId("workspace-migration-autopilots");
    expect(line).toHaveTextContent(
      "Automations: 2 imported, triggering as soon as the import finishes.",
    );
  });

  // A Desktop build older than the switches returns a report without the
  // summary; the card must not invent a count for it.
  // DENE-364: an agent the auto-bind rule already placed must say where it
  // went, instead of reappearing as "please bind this" after every import.
  it("shows the runtime the import already bound", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: false,
      report: {
        ...importReport,
        runtimes_to_bind: [
          {
            ...importReport.runtimes_to_bind[0]!,
            status: "bound" as const,
            bound_runtime_id: "r1",
            bound_runtime_name: "kunkun-mbp",
          },
        ],
      },
    });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));

    const section = await screen.findByTestId("workspace-migration-runtimes");
    expect(section).toHaveTextContent("Bound to kunkun-mbp.");
  });

  // The other half of the rule: several candidates must become one pick, not
  // one modal per agent, and none of them may be applied without a choice.
  it("collects the ambiguous picks and applies them in one request", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    const ambiguous = {
      ...importReport,
      runtimes_to_bind: [
        {
          ...importReport.runtimes_to_bind[0]!,
          candidate_ids: ["r1", "r2"],
          candidates: [
            {
              id: "r1",
              name: "kunkun-mbp",
              provider: "claude",
              runtime_mode: "local",
              profile_name: "default",
            },
            {
              id: "r2",
              name: "kunkun-mini",
              provider: "claude",
              runtime_mode: "local",
              profile_name: "default",
            },
          ],
        },
      ],
    };
    // The preview cannot bind anything (its agents do not exist yet), so the
    // first run is the dry run and the second is the apply.
    desktop.run.mockResolvedValueOnce({
      ok: true,
      action: "import",
      dryRun: true,
      report: ambiguous,
    });
    desktop.run.mockResolvedValueOnce({
      ok: true,
      action: "import",
      dryRun: false,
      report: ambiguous,
    });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));
    await screen.findByTestId("workspace-migration-report");
    // A preview offers no apply: nothing has been written yet.
    expect(
      screen.queryByRole("button", { name: "Apply bindings" }),
    ).not.toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Import into this workspace" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Import into this workspace" }),
    );

    // No runtime applied without a choice.
    expect(
      await screen.findByRole("button", { name: "Apply bindings" }),
    ).toBeDisabled();

    await user.click(screen.getByRole("combobox", { name: "Choose a runtime" }));
    await user.click(await screen.findByRole("option", { name: "kunkun-mini" }));

    desktop.run.mockResolvedValueOnce({
      ok: true,
      action: "bind-runtimes",
      report: {
        applied: true,
        bound: 1,
        failed: 0,
        bindings: [
          {
            agent_id: "a1",
            runtime_id: "r2",
            agent_name: "Builder",
            runtime_name: "kunkun-mini",
            bound: true,
            error_code: "",
            error: "",
          },
        ],
      },
    });
    await user.click(screen.getByRole("button", { name: "Apply bindings" }));

    await waitFor(() =>
      expect(desktop.run).toHaveBeenLastCalledWith({
        action: "bind-runtimes",
        workspace: "acme",
        bindings: [{ agentId: "a1", runtimeId: "r2" }],
      }),
    );
    expect(await screen.findByTestId("workspace-migration-runtimes")).toHaveTextContent(
      "Bound to kunkun-mini.",
    );
  });

  it("says what to connect when no runtime matches", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: false,
      report: {
        ...importReport,
        runtimes_to_bind: [
          {
            ...importReport.runtimes_to_bind[0]!,
            status: "no_candidate" as const,
            reason_code: "no_runtime_for_provider",
            reason: "the target workspace has no runtime with provider=claude",
            candidate_ids: [],
            candidates: [],
          },
        ],
      },
    });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));

    const section = await screen.findByTestId("workspace-migration-runtimes");
    expect(section).toHaveTextContent("provider=claude");
    expect(section).toHaveTextContent("Connect this machine's daemon");
  });

  it("stays quiet when the report carries no automation summary", async () => {
    const user = userEvent.setup();
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({
      ok: true,
      action: "import",
      dryRun: true,
      report: {
        secrets_to_fill: [],
        runtimes_to_bind: [],
        export_gaps: [],
        stats: { created: 0, updated: 0, renamed: 0, skipped: 0, failed: 0 },
      },
    });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));
    await screen.findByTestId("workspace-migration-report");
    expect(
      screen.queryByTestId("workspace-migration-autopilots"),
    ).not.toBeInTheDocument();
  });

  // A second click while the first export is still running used to come back
  // as "a transfer is already running" in red. The click is a no-op: the card
  // already shows the running transfer (DENE-318).
  it("treats a second click on a running transfer as a no-op", async () => {
    const user = userEvent.setup();
    desktop.pickExport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    let finish: (value: TransferRunResult) => void = () => {};
    desktop.run.mockImplementation(
      () =>
        new Promise<TransferRunResult>((resolve) => {
          finish = resolve;
        }),
    );
    renderCard();

    await user.click(screen.getByRole("button", { name: "Export to zip" }));
    await waitFor(() => expect(desktop.run).toHaveBeenCalledTimes(1));

    const running = screen.getByRole("button", { name: "Exporting…" });
    expect(running).toBeDisabled();
    fireEvent.click(running);
    fireEvent.click(running);
    expect(desktop.run).toHaveBeenCalledTimes(1);

    await act(async () => {
      finish({
        ok: false,
        code: "busy",
        message: "a transfer is already running",
      });
    });
    expect(
      screen.queryByTestId("workspace-migration-error"),
    ).not.toBeInTheDocument();
    expect(vi.mocked(toast.error)).not.toHaveBeenCalled();
  });

  it("shows session and attachment progress from structured events", async () => {
    const user = userEvent.setup();
    let emit: ((event: TransferProgressEvent) => void) | undefined;
    desktop.subscribe.mockImplementation((cb) => {
      emit = cb as (event: TransferProgressEvent) => void;
      return () => {};
    });
    desktop.pickExport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    let finish: (value: TransferRunResult) => void = () => {};
    desktop.run.mockImplementation(
      () =>
        new Promise<TransferRunResult>((resolve) => {
          finish = resolve;
        }),
    );
    renderCard();

    await user.click(screen.getByRole("button", { name: "Export to zip" }));
    await waitFor(() => expect(desktop.run).toHaveBeenCalled());
    act(() => {
      emit?.({
        phase: "running",
        sessionsTotal: 12,
        sessionsDone: 3,
        currentSessionTitle: "Deploy",
        attachmentsDownloaded: 4,
        attachmentsTotal: 8,
      });
    });
    const progress = await screen.findByTestId("workspace-migration-progress");
    expect(progress).toHaveTextContent("3 / 12 sessions");
    expect(progress).toHaveTextContent("Current session: Deploy");
    expect(progress).toHaveTextContent("4 / 8 attachments");

    // The point of the progress pipe is that the numbers move while the
    // export runs; a card stuck on its first sample is what looked hung.
    act(() => {
      emit?.({
        phase: "running",
        sessionsTotal: 12,
        sessionsDone: 7,
        currentSessionTitle: "Review",
        attachmentsDownloaded: 6,
        attachmentsTotal: 8,
      });
    });
    expect(progress).toHaveTextContent("7 / 12 sessions");
    expect(progress).toHaveTextContent("Current session: Review");
    expect(progress).toHaveTextContent("6 / 8 attachments");

    await act(async () => {
      finish({
        ok: true,
        action: "export",
        outPath: "/tmp/acme.zip",
        bytes: 100,
      });
    });
  });

  // Importing 29 attachments is minutes of uploading; the card must show that
  // while it applies, not only while it exports (DENE-318).
  it("shows attachment progress while an import applies", async () => {
    const user = userEvent.setup();
    let emit: ((event: TransferProgressEvent) => void) | undefined;
    desktop.subscribe.mockImplementation((cb) => {
      emit = cb as (event: TransferProgressEvent) => void;
      return () => {};
    });
    desktop.pickImport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    let finish: (value: TransferRunResult) => void = () => {};
    desktop.run.mockResolvedValueOnce({
      ok: true,
      action: "import",
      dryRun: true,
      report: importReport,
    });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Import from zip" }));
    await screen.findByTestId("workspace-migration-report");

    desktop.run.mockImplementationOnce(
      () =>
        new Promise<TransferRunResult>((resolve) => {
          finish = resolve;
        }),
    );
    await user.click(
      screen.getByRole("button", { name: "Import into this workspace" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Import into this workspace" }),
    );
    await waitFor(() => expect(desktop.run).toHaveBeenCalledTimes(2));

    act(() => {
      emit?.({
        phase: "running",
        attachmentsDownloaded: 3,
        attachmentsTotal: 29,
      });
    });
    expect(
      await screen.findByTestId("workspace-migration-progress"),
    ).toHaveTextContent("3 / 29 attachments");

    await act(async () => {
      finish({
        ok: true,
        action: "import",
        dryRun: false,
        report: importReport,
      });
    });
  });

  it.each([
    ["target_unsupported", "The target instance needs a kun build."],
    ["cli_too_old", "The Desktop CLI is too old for transfer."],
    ["transfer_bundle_corrupt", "This zip is not a valid transfer bundle."],
  ] as const)("shows a readable error for %s", async (code, message) => {
    const user = userEvent.setup();
    desktop.pickExport.mockResolvedValue({
      ok: true,
      path: "/tmp/acme.zip",
      fileName: "acme.zip",
    });
    desktop.run.mockResolvedValue({ ok: false, code, message: code });
    renderCard();

    await user.click(screen.getByRole("button", { name: "Export to zip" }));

    expect(await screen.findByTestId("workspace-migration-error")).toHaveTextContent(
      message,
    );
  });
});

describe("ConfigTransferTab web hide", () => {
  it("does not render the migration card on web", () => {
    desktop.isDesktop = false;
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    renderWithI18n(
      <QueryClientProvider client={client}>
        <ConfigTransferTab />
      </QueryClientProvider>,
    );
    expect(screen.queryByTestId("workspace-migration-card")).not.toBeInTheDocument();
    expect(
      screen.queryByText("Migrate across environments (including chats)"),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Export configuration" }),
    ).toBeInTheDocument();
  });
});
