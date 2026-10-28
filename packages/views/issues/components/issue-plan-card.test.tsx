import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { configStore } from "@multica/core/config";
import { OFFICE_PLAN_FIRST_FLAG } from "@multica/core/feature-flags";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssuePlan, IssuePlansResponse } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const apiMock = vi.hoisted(() => ({
  listIssuePlans: vi.fn(),
  approveIssuePlan: vi.fn(),
  requestIssuePlanChanges: vi.fn(),
}));
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: apiMock,
}));
const toastMock = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast: toastMock }));

import { IssuePlanCard } from "./issue-plan-card";

function plan(overrides: Partial<IssuePlan> = {}): IssuePlan {
  return {
    id: "plan-2",
    issue_id: "issue-1",
    version: 2,
    status: "proposed",
    author_type: "agent",
    author_id: "agent-1",
    goal: "Add plan cards",
    out_of_scope: "No mobile",
    steps: [{ title: "Server" }, { title: "UI" }],
    done_when: "Tests pass",
    comment_id: "comment-1",
    approved_by: null,
    approved_at: null,
    changes_requested_by: null,
    changes_requested_at: null,
    created_at: "2026-10-27T00:00:00Z",
    ...overrides,
  };
}

function respond(overrides: Partial<IssuePlansResponse> = {}) {
  apiMock.listIssuePlans.mockResolvedValue({
    gated: true,
    approved: false,
    can_approve: true,
    plans: [plan()],
    ...overrides,
  });
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <IssuePlanCard issueId="issue-1" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  configStore.getState().setLocalFeatureFlags({ [OFFICE_PLAN_FIRST_FLAG]: true });
});

afterEach(() => {
  configStore.getState().setFeatureFlags({});
  configStore.getState().setLocalFeatureFlags({});
});

describe("IssuePlanCard", () => {
  it("shows the newest plan with approve and change-it buttons for a reviewer", async () => {
    respond();
    apiMock.approveIssuePlan.mockResolvedValue(plan({ status: "approved" }));
    renderCard();
    expect(await screen.findByText("Plan v2")).toBeInTheDocument();
    expect(screen.getByText("Waiting for approval")).toBeInTheDocument();
    expect(screen.getByText("Add plan cards")).toBeInTheDocument();
    expect(screen.getByText("UI")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve and start" }));
    await waitFor(() => expect(apiMock.approveIssuePlan).toHaveBeenCalledWith("issue-1", 2));
    await waitFor(() => expect(toastMock.success).toHaveBeenCalled());
  });

  it("says when the approval woke no agent and offers to wake it again", async () => {
    respond();
    const approved = { gated: true, approved: true, can_approve: true, plans: [plan({ status: "approved" })] };
    apiMock.listIssuePlans.mockResolvedValueOnce({ ...approved, approved: false, plans: [plan()] });
    apiMock.listIssuePlans.mockResolvedValue(approved);
    apiMock.approveIssuePlan.mockResolvedValue({ ...plan({ status: "approved" }), wake: { none_woken: true } });
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: "Approve and start" }));
    await waitFor(() =>
      expect(toastMock.error).toHaveBeenCalledWith("Approved, but no agent was woken. Wake it again to continue."),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Wake the agent again" }));
    await waitFor(() => expect(apiMock.approveIssuePlan).toHaveBeenCalledTimes(2));
  });

  it("sends the plan back only with a note", async () => {
    respond();
    apiMock.requestIssuePlanChanges.mockResolvedValue(plan());
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: "Change it first" }));
    const send = screen.getByRole("button", { name: "Send back" });
    expect(send).toBeDisabled();
    fireEvent.change(screen.getByLabelText("What should change?"), { target: { value: "Drop UI" } });
    fireEvent.click(send);
    await waitFor(() =>
      expect(apiMock.requestIssuePlanChanges).toHaveBeenCalledWith("issue-1", 2, "Drop UI"),
    );
  });

  it("hides the buttons for someone who cannot answer, and once approved", async () => {
    respond({ can_approve: false });
    const { unmount } = renderCard();
    expect(await screen.findByText("Plan v2")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve and start" })).toBeNull();
    unmount();

    respond({ approved: true, plans: [plan({ status: "approved" })] });
    renderCard();
    expect(await screen.findByText("Approved")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve and start" })).toBeNull();
  });

  it("says the agent will propose a plan when there is none yet", async () => {
    respond({ plans: [] });
    renderCard();
    expect(await screen.findByText("Plan first")).toBeInTheDocument();
  });

  it("renders nothing on an issue that does not ask for a plan, or without the flag", async () => {
    respond({ gated: false, plans: [] });
    const { container, unmount } = renderCard();
    await waitFor(() => expect(apiMock.listIssuePlans).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
    unmount();

    configStore.getState().setLocalFeatureFlags({});
    respond();
    const second = renderCard();
    expect(second.container).toBeEmptyDOMElement();
    expect(apiMock.listIssuePlans).toHaveBeenCalledTimes(1);
  });
});
