// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import { configStore } from "@multica/core/config";
import { OFFICE_DEFAULT_DIR_FLAG } from "@multica/core/feature-flags";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockState = vi.hoisted(() => ({ updateRuntime: vi.fn(), userId: "owner-1" }));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return { ...actual, api: { ...actual.api, updateRuntime: mockState.updateRuntime } };
});
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: { user: { id: string } }) => unknown) => selector({ user: { id: mockState.userId } }),
}));

import { MachineDefaultDirControl } from "./machine-default-dir-control";

function runtime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Claude (dev.local)",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "dev.local",
    metadata: {},
    owner_id: "owner-1",
    visibility: "private",
    last_seen_at: "2026-10-27T00:00:00Z",
    created_at: "2026-10-27T00:00:00Z",
    updated_at: "2026-10-27T00:00:00Z",
    ...overrides,
  };
}

function render(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return renderWithI18n(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
  mockState.userId = "owner-1";
  mockState.updateRuntime.mockResolvedValue(runtime());
  configStore.getState().setLocalFeatureFlags({ [OFFICE_DEFAULT_DIR_FLAG]: true });
});

afterEach(() => {
  configStore.getState().setLocalFeatureFlags({});
});

describe("MachineDefaultDirControl", () => {
  it("renders nothing without the office flag", () => {
    configStore.getState().setLocalFeatureFlags({});
    render(<MachineDefaultDirControl runtimes={[runtime()]} />);
    expect(screen.queryByTestId("machine-default-dir")).toBeNull();
  });

  it("renders nothing for someone else's machine or a cloud runtime", () => {
    mockState.userId = "member-2";
    const { unmount } = render(<MachineDefaultDirControl runtimes={[runtime()]} />);
    expect(screen.queryByTestId("machine-default-dir")).toBeNull();
    unmount();
    mockState.userId = "owner-1";
    render(<MachineDefaultDirControl runtimes={[runtime({ runtime_mode: "cloud" })]} />);
    expect(screen.queryByTestId("machine-default-dir")).toBeNull();
  });

  it("saves the trimmed path through the runtime PATCH", async () => {
    render(<MachineDefaultDirControl runtimes={[runtime()]} />);
    fireEvent.change(screen.getByLabelText("Default working directory"), { target: { value: "  /Users/me/work " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockState.updateRuntime).toHaveBeenCalledWith("runtime-1", { default_work_dir: "/Users/me/work" }));
  });

  it("refuses a relative path before sending it", () => {
    render(<MachineDefaultDirControl runtimes={[runtime()]} />);
    fireEvent.change(screen.getByLabelText("Default working directory"), { target: { value: "work" } });
    expect(screen.getByRole("alert").textContent).toContain("Enter a full path");
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
  });

  it("shows whether the machine found the saved folder", () => {
    const { unmount } = render(
      <MachineDefaultDirControl runtimes={[runtime({ default_work_dir: { path: "/Users/me/gone", exists: false } })]} />,
    );
    expect(screen.getByRole("alert").textContent).toContain("isn't on this machine");
    unmount();
    const found = render(
      <MachineDefaultDirControl runtimes={[runtime({ default_work_dir: { path: "/Users/me/work", exists: true } })]} />,
    );
    expect(screen.getByRole("status").textContent).toContain("Found on this machine");
    found.unmount();
    render(
      <MachineDefaultDirControl runtimes={[runtime({ status: "offline", default_work_dir: { path: "/Users/me/work", exists: null } })]} />,
    );
    expect(screen.getByRole("status").textContent).toContain("when it comes online");
  });

  it("clears the saved path with an empty value", async () => {
    render(<MachineDefaultDirControl runtimes={[runtime({ default_work_dir: { path: "/Users/me/work", exists: true } })]} />);
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(mockState.updateRuntime).toHaveBeenCalledWith("runtime-1", { default_work_dir: "" }));
  });

  it("explains a path the server rejected", async () => {
    mockState.updateRuntime.mockRejectedValue(
      new ApiError("bad", 400, "Bad Request", { code: "invalid_default_work_dir", error: "default_work_dir must be an absolute path" }),
    );
    render(<MachineDefaultDirControl runtimes={[runtime()]} />);
    fireEvent.change(screen.getByLabelText("Default working directory"), { target: { value: "C:\\work" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toContain("The server rejected this path");
  });
});
