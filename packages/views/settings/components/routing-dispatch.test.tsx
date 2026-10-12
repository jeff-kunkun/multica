import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const getRoutingDispatch = vi.hoisted(() => vi.fn());
const previewRoutingDispatch = vi.hoisted(() => vi.fn());
const updateRoutingDispatch = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: { getRoutingDispatch, previewRoutingDispatch, updateRoutingDispatch },
  };
});

import { RoutingDispatchSection } from "./routing-dispatch";

const tiers = [
  { key: "weak", label: "弱档" },
  { key: "medium", label: "中档" },
  { key: "strong", label: "强档" },
];

function view(overrides: Record<string, unknown> = {}) {
  return {
    policy: {
      text: "前端交给 Claude。文档沉淀最多中档。",
      rules: [
        { id: "rule_1", category: "前端", series: "Claude", source: "前端交给 Claude。", origin: "default" },
        { id: "rule_2", category: "文档沉淀", max_tier: "medium", source: "文档沉淀最多中档。", origin: "default" },
      ],
    },
    is_default: true,
    series: ["Claude", "Codex"],
    tiers,
    editable: true,
    ...overrides,
  };
}

function render(canManage = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <RoutingDispatchSection wsId="ws-1" canManage={canManage} />
    </QueryClientProvider>,
  );
}

describe("RoutingDispatchSection", () => {
  beforeEach(() => {
    getRoutingDispatch.mockReset().mockResolvedValue(view());
    previewRoutingDispatch.mockReset();
    updateRoutingDispatch.mockReset();
    toastError.mockReset();
  });

  it("previews parsed rows, lets a hand edit clear the origin, and only writes on Apply", async () => {
    render();
    await screen.findByText("From: “前端交给 Claude。”");
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
    expect(updateRoutingDispatch).not.toHaveBeenCalled();

    previewRoutingDispatch.mockResolvedValue({
      policy: {
        text: "审查交给 Codex。",
        rules: [{ id: "rule_1", category: "审查", series: "Codex", source: "审查交给 Codex。", origin: "model" }],
      },
      dropped: ["row 2: no category"],
    });
    const box = screen.getByRole("textbox", { name: "Source text" });
    await userEvent.clear(box);
    await userEvent.type(box, "审查交给 Codex。");
    await userEvent.click(screen.getByRole("button", { name: "Parse preview" }));
    await screen.findByText("From: “审查交给 Codex。”");
    expect(previewRoutingDispatch).toHaveBeenCalledWith("ws-1", "审查交给 Codex。");
    expect(screen.getByText("Not read: row 2: no category")).toBeInTheDocument();
    expect(screen.getByText("parsed")).toBeInTheDocument();
    expect(updateRoutingDispatch).not.toHaveBeenCalled();

    // Changing the tier by hand drops the row's model origin.
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Tier cap" }), "medium");
    expect(screen.getByText("edited")).toBeInTheDocument();

    updateRoutingDispatch.mockResolvedValue(
      view({
        is_default: false,
        policy: {
          text: "审查交给 Codex。",
          version: 1,
          rules: [{ id: "rule_1", category: "审查", series: "Codex", max_tier: "medium", source: "审查交给 Codex。", origin: "manual" }],
          history: [{ version: 1, at: "2026-10-11T08:00:00Z", by: "Kun", rules_diff: "+ 审查→Codex 封顶中档" }],
        },
      }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(updateRoutingDispatch).toHaveBeenCalledWith("ws-1", {
        text: "审查交给 Codex。",
        rules: [{ id: "rule_1", category: "审查", series: "Codex", max_tier: "medium", source: "审查交给 Codex。", origin: "" }],
      }),
    );
    await screen.findByText("Version 1");
    expect(screen.getByText("+ 审查→Codex 封顶中档")).toBeInTheDocument();
    expect(screen.getByText("Version 1 in effect")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore default" })).toBeInTheDocument();
  });

  it("explains why a member or an agent run cannot edit", async () => {
    render(false);
    await screen.findByText("Only an admin can change this.");
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(screen.getByRole("textbox", { name: "Source text" })).toBeDisabled();

    getRoutingDispatch.mockResolvedValue(view({ editable: false }));
    render(true);
    await screen.findByText("An agent can read this but not change it.");
  });

  it("surfaces a refused preview without touching the rows", async () => {
    render();
    await screen.findByText("From: “前端交给 Claude。”");
    previewRoutingDispatch.mockRejectedValue(new Error("routing judge unavailable"));
    await userEvent.click(screen.getByRole("button", { name: "Parse preview" }));
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("routing judge unavailable"));
    expect(screen.getByText("From: “前端交给 Claude。”")).toBeInTheDocument();
  });
});
