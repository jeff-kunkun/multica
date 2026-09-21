// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { LogRepositorySection } from "./log-repository-section";

const getLogExportConfig = vi.hoisted(() => vi.fn());
const updateLogExportConfig = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return { ...actual, api: { getLogExportConfig, updateLogExportConfig } };
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const stored = {
  repo_url: "https://github.com/acme/logs",
  branch: "main",
  has_token: true,
  token_storable: true,
};

function renderSection(canManage = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <LogRepositorySection wsId="ws-1" canManage={canManage} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("LogRepositorySection", () => {
  // The token is write-only: a save that did not touch it must not send the
  // field at all, or the server would read "" as "clear the stored token".
  it("keeps the stored token when only the branch changes", async () => {
    getLogExportConfig.mockResolvedValue(stored);
    updateLogExportConfig.mockResolvedValue({ ...stored, branch: "logs" });
    renderSection();

    const branch = await screen.findByDisplayValue("main");
    expect(screen.getByLabelText("Access token")).toHaveValue("");
    fireEvent.change(branch, { target: { value: "logs" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(updateLogExportConfig).toHaveBeenCalledTimes(1));
    expect(updateLogExportConfig).toHaveBeenCalledWith("ws-1", {
      repo_url: "https://github.com/acme/logs",
      branch: "logs",
    });
  });

  it("sends a typed token, and an empty one only when the token is removed", async () => {
    getLogExportConfig.mockResolvedValue(stored);
    updateLogExportConfig.mockResolvedValue(stored);
    renderSection();

    await screen.findByDisplayValue("main");
    fireEvent.change(screen.getByLabelText("Access token"), { target: { value: " new-token " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(updateLogExportConfig).toHaveBeenLastCalledWith("ws-1", {
        repo_url: stored.repo_url,
        branch: "main",
        token: "new-token",
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Remove token" }));
    await waitFor(() =>
      expect(updateLogExportConfig).toHaveBeenLastCalledWith("ws-1", {
        repo_url: stored.repo_url,
        branch: "main",
        token: "",
      }),
    );
  });

  it("is read-only, with no token field, for a member who cannot manage the workspace", async () => {
    getLogExportConfig.mockResolvedValue(stored);
    renderSection(false);

    expect(await screen.findByDisplayValue("main")).toBeDisabled();
    expect(screen.queryByLabelText("Access token")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });
});
