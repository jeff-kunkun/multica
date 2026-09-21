// @vitest-environment jsdom

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import type { LogExportPreview } from "@multica/core/api";
import { renderWithI18n } from "../../test/i18n";
import { LogExportDialog } from "./log-export-dialog";

const previewTaskLogExport = vi.hoisted(() => vi.fn());
const reportTaskLogExport = vi.hoisted(() => vi.fn());
const downloadTaskLogExport = vi.hoisted(() => vi.fn());
const copyText = vi.hoisted(() => vi.fn(async (_text: string) => true));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: { previewTaskLogExport, reportTaskLogExport, downloadTaskLogExport },
  };
});

vi.mock("@multica/ui/lib/clipboard", () => ({ copyText }));

const bundle: LogExportPreview = {
  empty: false,
  filename: "multica-logs-dene-599-7f81fd6-run.zip",
  size_bytes: 4096,
  summary: "# Run summary\nexit code 1",
  meta: {
    scope: "run",
    run_count: 1,
    entry_count: 42,
    dropped_entries: 0,
    partial: false,
    warnings: [],
    runs: [],
  },
  log_repo_configured: false,
};

const emptyPreview: LogExportPreview = { ...bundle, empty: true, filename: "" };

function renderDialog(canReport = true) {
  return renderWithI18n(
    <LogExportDialog open onOpenChange={() => {}} taskId="task-1" canReport={canReport} />,
  );
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("LogExportDialog", () => {
  it("exports the chosen scope, then copies the summary and reports the same scope", async () => {
    previewTaskLogExport.mockResolvedValue(bundle);
    reportTaskLogExport.mockResolvedValue({
      comment_id: "c-1",
      issue_id: "i-1",
      issue_identifier: "DENE-599",
      delivery: "attachment",
      link: "",
      fallback_reason: "log repository rejected the push (HTTP 401)",
      filename: bundle.filename,
      size_bytes: 4096,
      mentioned: "Kun",
    });
    renderDialog();

    fireEvent.click(screen.getByRole("radio", { name: "Last N hours" }));
    fireEvent.change(screen.getByLabelText("Hours to export"), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "Export" }));

    expect(await screen.findByText(bundle.filename)).toBeInTheDocument();
    expect(previewTaskLogExport).toHaveBeenCalledWith("task-1", {
      scope: "hours",
      hours: 12,
      allowPartial: false,
    });
    expect(screen.getByText(/entries: 42/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Copy summary for AI" }));
    await waitFor(() => expect(copyText).toHaveBeenCalledWith(bundle.summary));

    fireEvent.click(screen.getByRole("button", { name: "Report as comment and @assignee" }));
    expect(await screen.findByText("Reported to DENE-599")).toBeInTheDocument();
    expect(reportTaskLogExport).toHaveBeenCalledWith("task-1", {
      scope: "hours",
      hours: 12,
      allowPartial: false,
    });
    expect(screen.getByText("Kun was mentioned.")).toBeInTheDocument();
    // A failed repository push is said out loud, not hidden behind "reported".
    expect(screen.getByText(/attached instead: log repository rejected/)).toBeInTheDocument();
  });

  it("offers a wider scope when the period has no logs", async () => {
    previewTaskLogExport.mockResolvedValueOnce(emptyPreview).mockResolvedValueOnce(bundle);
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByText("No logs in this period")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Widen to the whole issue" }));
    expect(await screen.findByText(bundle.filename)).toBeInTheDocument();
    expect(previewTaskLogExport).toHaveBeenLastCalledWith("task-1", {
      scope: "task",
      hours: 6,
      allowPartial: false,
    });
    expect(screen.getByRole("radio", { name: "Whole issue" })).toHaveAttribute("aria-checked", "true");
  });

  it("shows the failure reason with retry, and the partial export when the server collected some", async () => {
    previewTaskLogExport
      .mockRejectedValueOnce(
        new ApiError("failed to read 1 run", 500, "Internal Server Error", {
          code: "partial_available",
          collected_entries: 17,
        }),
      )
      .mockResolvedValueOnce({ ...bundle, meta: { ...bundle.meta, partial: true } });
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByText("failed to read 1 run")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Export only what was collected (17 entries)" }));
    expect(await screen.findByText(/Partial export/)).toBeInTheDocument();
    expect(previewTaskLogExport).toHaveBeenLastCalledWith("task-1", {
      scope: "run",
      hours: 6,
      allowPartial: true,
    });
  });

  it("has no partial export to offer on a plain failure, and no report action without an issue", async () => {
    previewTaskLogExport.mockRejectedValueOnce(new Error("network down")).mockResolvedValueOnce(bundle);
    renderDialog(false);

    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByText("network down")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Export only what was collected/ })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText(bundle.filename)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Report as comment and @assignee" })).toBeNull();
  });
});
