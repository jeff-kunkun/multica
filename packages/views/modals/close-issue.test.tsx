import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import { renderWithI18n } from "../test/i18n";
import { CloseIssueDialog } from "./close-issue";

const mockClose = vi.fn();

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useCloseIssue: () => ({ mutateAsync: mockClose }),
}));

vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      listProjectMemoryLocations: vi.fn(async () => ({
        locations: [
          { key: "agents", path: "AGENTS.md", kind: "file" },
          { key: "context", path: "CONTEXT.md", kind: "file" },
        ],
      })),
    },
  };
});

function renderDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onClose = vi.fn();
  renderWithI18n(
    <QueryClientProvider client={client}>
      <CloseIssueDialog
        onClose={onClose}
        data={{ issueId: "issue-1", identifier: "DENE-972" }}
      />
    </QueryClientProvider>,
  );
  return onClose;
}

describe("CloseIssueDialog", () => {
  beforeEach(() => {
    mockClose.mockReset();
    mockClose.mockResolvedValue({});
  });

  it("submits an explicit no-qualified-knowledge audit", async () => {
    const onClose = renderDialog();
    fireEvent.change(screen.getByPlaceholderText("What this close rests on"), {
      target: { value: "docs only" },
    });
    fireEvent.click(screen.getByLabelText("No qualified knowledge"));
    fireEvent.click(screen.getByRole("button", { name: "Close out" }));

    await waitFor(() => expect(mockClose).toHaveBeenCalledTimes(1));
    expect(mockClose).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "issue-1",
        outcome: "done",
        evidence: "docs only",
        knowledge_audit: { none: true },
      }),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("shows the server's rejection when the audit is missing", async () => {
    mockClose.mockRejectedValueOnce(
      new ApiError(
        "缺知识审计：用 --knowledge-none 声明无够格知识，或用 --knowledge <位置>=<摘要> 写明改了项目记忆的哪一处",
        400,
        "Bad Request",
      ),
    );
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText("What this close rests on"), {
      target: { value: "docs only" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Close out" }));

    expect(
      await screen.findByRole("alert"),
    ).toHaveTextContent("缺知识审计：用 --knowledge-none");
  });

  it("offers all seven outcomes and shows what the picked one needs (DENE-1002)", () => {
    renderDialog();
    const select = screen.getByRole("combobox");
    expect(Array.from(select.querySelectorAll("option")).map((o) => o.value)).toEqual([
      "done",
      "in_review",
      "blocked",
      "cancelled",
      "backlog",
      "todo",
      "in_progress",
    ]);
    expect(screen.getByText(/Needs the delivery evidence/)).toBeInTheDocument();

    fireEvent.change(select, { target: { value: "in_progress" } });
    expect(screen.getByText(/who continues/)).toBeInTheDocument();
    expect(screen.getByPlaceholderText("2026-01-01T09:00:00Z")).toBeInTheDocument();

    fireEvent.change(select, { target: { value: "backlog" } });
    expect(screen.getByText(/why it goes back to planning/)).toBeInTheDocument();
    expect(
      screen.queryByPlaceholderText("2026-01-01T09:00:00Z"),
    ).not.toBeInTheDocument();
  });

  it("sends a deferred close with no wake fields", async () => {
    renderDialog();
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "todo" } });
    fireEvent.change(screen.getByPlaceholderText("What this close rests on"), {
      target: { value: "需求还没定，先放回待办" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Close out" }));

    await waitFor(() =>
      expect(mockClose).toHaveBeenCalledWith(
        expect.objectContaining({
          id: "issue-1",
          outcome: "todo",
          evidence: "需求还没定，先放回待办",
          blocked_by: undefined,
          wake_at: undefined,
        }),
      ),
    );
  });

  it("sends who continues on an in_progress close", async () => {
    renderDialog();
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "in_progress" } });
    fireEvent.change(screen.getByPlaceholderText("What this close rests on"), {
      target: { value: "网关改造做到一半，等扩容窗口" },
    });
    fireEvent.change(screen.getByPlaceholderText("2026-01-01T09:00:00Z"), {
      target: { value: "2026-01-01T09:00:00Z" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Close out" }));

    await waitFor(() =>
      expect(mockClose).toHaveBeenCalledWith(
        expect.objectContaining({
          id: "issue-1",
          outcome: "in_progress",
          evidence: "网关改造做到一半，等扩容窗口",
          wake_at: "2026-01-01T09:00:00Z",
        }),
      ),
    );
  });

  it("submits the checklist location the person checked", async () => {
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText("What this close rests on"), {
      target: { value: "seeded agents" },
    });
    fireEvent.click(screen.getByLabelText("Wrote project memory"));
    fireEvent.click(await screen.findByRole("checkbox", { name: /AGENTS.md/ }));
    fireEvent.change(screen.getByLabelText("agents"), {
      target: { value: "补了开张种子" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Close out" }));

    await waitFor(() =>
      expect(mockClose).toHaveBeenCalledWith(
        expect.objectContaining({
          knowledge_audit: {
            changes: [{ location: "agents", summary: "补了开张种子" }],
          },
        }),
      ),
    );
  });
});
