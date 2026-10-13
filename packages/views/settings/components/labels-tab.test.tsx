import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { LabelsTab } from "./labels-tab";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

const queried = vi.hoisted(() => ({ keys: [] as unknown[][] }));
vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: (options: { queryKey: unknown[] }) => {
    queried.keys.push(options.queryKey);
    return { data: [], isLoading: false };
  },
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme" }),
}));

vi.mock("@multica/core/labels", () => ({
  labelListOptions: (wsId: string, resourceType: string) => ({
    queryKey: ["labels", wsId, "list", resourceType],
  }),
  useCreateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteLabel: () => ({ mutate: vi.fn(), isPending: false }),
}));

describe("Label catalog", () => {
  afterEach(() => {
    cleanup();
    queried.keys = [];
  });

  // Agent labels were removed from the product (MUL-5600) even though the
  // backend still models the `agent` resource type.
  it("manages only the issue catalog", () => {
    renderWithI18n(<LabelsTab />);

    expect(queried.keys).toContainEqual(["labels", "workspace-1", "list", "issue"]);
    expect(queried.keys.some((key) => key.includes("agent"))).toBe(false);
    expect(screen.queryByRole("tab")).toBeNull();
    expect(screen.getByRole("button", { name: /New label/ })).toBeInTheDocument();
  });
});
