import { type ComponentProps, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enModals from "../locales/en/modals.json";
import { SimilarIssuesNotice } from "./similar-issues";

const similarIssues = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({ api: { similarIssues } }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }) }));
vi.mock("../navigation", async (importOriginal) => ({
  ...await importOriginal<typeof import("../navigation")>(),
  AppLink: ({ href, children, onClick, ...props }: ComponentProps<"a">) => (
    <a href={href} {...props} onClick={(event) => { onClick?.(event); event.preventDefault(); }}>{children}</a>
  ),
}));

describe("SimilarIssuesNotice", () => {
  it("shows server candidates before submission and clears them when the project changes", async () => {
    similarIssues.mockResolvedValue({
      candidates: [{ id: "old-work", identifier: "DENE-2904", title: "Already tested this deployment", status: "done", score: 0.8, why: "shared MR", recent_done: true }],
      would_block: true, threshold: 0.4,
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>
        <I18nProvider locale="en" resources={{ en: { modals: enModals } }}>
          {children}
        </I18nProvider>
      </QueryClientProvider>
    );
    const onNavigate = vi.fn();
    const view = render(<SimilarIssuesNotice title="Test deployment again" description="!494" projectId="project-a" onNavigate={onNavigate} />, { wrapper });
    const candidate = await screen.findByRole("link", { name: /DENE-2904/ });
    expect(candidate).toHaveAttribute("href", "/issues/old-work");
    expect(screen.getByText("Recently completed")).toBeVisible();
    fireEvent.click(candidate, { ctrlKey: true });
    expect(onNavigate).not.toHaveBeenCalled();
    fireEvent.click(candidate);
    expect(onNavigate).toHaveBeenCalledOnce();
    expect(similarIssues).toHaveBeenCalledWith(expect.objectContaining({ title: "Test deployment again", project_id: "project-a", description: "!494" }));
    similarIssues.mockResolvedValue({ candidates: [], would_block: false, threshold: 0.4 });
    view.rerender(<SimilarIssuesNotice title="Test deployment again" description="!494" projectId="project-b" />);
    expect(screen.queryByRole("link", { name: /DENE-2904/ })).not.toBeInTheDocument();
    await waitFor(() => expect(similarIssues).toHaveBeenCalledWith(expect.objectContaining({ project_id: "project-b" })), { timeout: 2000 });
    view.unmount();
    queryClient.clear();
  });
});
