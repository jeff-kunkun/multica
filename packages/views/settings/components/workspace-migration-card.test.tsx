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
      candidate_ids: ["r1"],
    },
  ],
  export_gaps: [{ group: "plugins", reason: "read_api_missing", status: 404 }],
  stats: { created: 3, updated: 1, renamed: 0, skipped: 0, failed: 0 },
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
});

describe("WorkspaceMigrationCard", () => {
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
    expect(screen.getByText("Builder · claude · local · default")).toBeInTheDocument();
    expect(screen.getByText("plugins · read_api_missing")).toBeInTheDocument();
    expect(screen.getByText("New")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(desktop.run).toHaveBeenCalledWith({
      action: "import",
      workspace: "acme",
      inPath: "/tmp/acme.zip",
      dryRun: true,
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
      }),
    );
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
    await act(async () => {
      finish({
        ok: true,
        action: "export",
        outPath: "/tmp/acme.zip",
        bytes: 100,
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
