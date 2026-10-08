import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ProjectReport } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ChatReportBar } from "./chat-report-bar";

const report = vi.hoisted(() => ({ current: null as ProjectReport | null }));

vi.mock("@multica/core/projects", () => ({
  projectReportOptions: (wsId: string, id: string) => ({
    queryKey: ["projects", wsId, "report", id],
    queryFn: async () => report.current,
  }),
}));
vi.mock("@multica/core/issue-statuses", () => ({
  useIssueStatuses: () => ({ labelOf: (key: string) => key }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/ws/issues/${id}` }),
}));
vi.mock("../../navigation", () => ({
  AppLink: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => (
    <a href={href} {...rest}>{children}</a>
  ),
}));
vi.mock("@multica/ui/hooks/use-mobile", () => ({ useIsMobile: () => false }));

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();

function makeReport(items: ProjectReport["items"]): ProjectReport {
  return {
    project_id: "p1",
    project_title: "Alpha",
    since: iso(3_600_000),
    until: iso(0),
    last_heard_at: iso(3_600_000),
    items,
    counts: { total: items.length, done: 0, in_progress: 0, waiting_you: 0 },
    actions: [],
    marked: false,
    inbox_read: 0,
  };
}

function renderBar(onHear = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithI18n(
    <QueryClientProvider client={qc}>
      <ChatReportBar wsId="ws" userId="u1" sessionId="chat-a" projectIds={["p1"]} disabled={false} onHear={onHear} />
    </QueryClientProvider>,
  );
  return onHear;
}

describe("ChatReportBar", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("stays hidden when nothing moved since the last report", async () => {
    report.current = makeReport([]);
    renderBar();
    await waitFor(() => expect(screen.queryByText(/new updates/)).toBeNull());
  });

  it("counts the news, lists fresh moves first and asks for the report", async () => {
    // The window was last opened 30 minutes ago: only the 10-minute-old move is fresh.
    localStorage.setItem("multica:chat-report-opened:u1:p1", iso(30 * 60_000));
    report.current = makeReport([
      { issue_id: "i1", identifier: "DENE-1", title: "Old move", status: "done", priority: "medium", from_status: "todo", opened: false, changed_at: iso(50 * 60_000), phase: "done", needs_you: false, source_chat: { id: "chat-a", title: "A", accessible: true } },
      { issue_id: "i2", identifier: "DENE-2", title: "Fresh move", status: "in_review", priority: "medium", from_status: "todo", opened: false, changed_at: iso(10 * 60_000), phase: "waiting_you", needs_you: true, source_chat: { id: "chat-b", title: "B chat", accessible: true } },
    ]);
    const onHear = renderBar();

    const trigger = await screen.findByText(/2 new updates/);
    expect(screen.getByText(/1 need you/)).toBeTruthy();
    fireEvent.click(trigger);

    const rows = await screen.findAllByRole("link");
    expect(rows[0]?.textContent).toContain("Fresh move");
    expect(rows[0]?.textContent).toContain("Just changed");
    expect(rows[0]?.textContent).toContain("todo → in_review");
    expect(rows[0]?.textContent).toContain('from "B chat"');
    // The chat's own tickets don't name it as their source.
    expect(rows[1]?.textContent).not.toContain("from");

    fireEvent.click(screen.getAllByRole("button", { name: "Hear report" })[0]!);
    expect(onHear).toHaveBeenCalledWith(expect.stringContaining('"Alpha"'));
  });
});
