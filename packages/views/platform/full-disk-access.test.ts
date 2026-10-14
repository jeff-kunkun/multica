import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useFullDiskAccess, type FullDiskAccess } from "./full-disk-access";

function installDesktopAPI(api: object) {
  (window as unknown as { desktopAPI?: object }).desktopAPI = api;
}

afterEach(() => {
  delete (window as unknown as { desktopAPI?: object }).desktopAPI;
});

describe("useFullDiskAccess", () => {
  it("stays unsupported on web", () => {
    const { result } = renderHook(() => useFullDiskAccess());
    expect(result.current.access.status).toBe("unsupported");
  });

  it("rechecks when the window regains focus", async () => {
    const states: FullDiskAccess[] = [
      { status: "missing", appName: "Multica Office" },
      { status: "granted", appName: "Multica Office" },
    ];
    const getFullDiskAccess = vi.fn(async () => states.shift()!);
    installDesktopAPI({ getFullDiskAccess });

    const { result } = renderHook(() => useFullDiskAccess());
    await waitFor(() => expect(result.current.access.status).toBe("missing"));

    act(() => {
      window.dispatchEvent(new Event("focus"));
    });
    await waitFor(() => expect(result.current.access.status).toBe("granted"));
    expect(getFullDiskAccess).toHaveBeenCalledTimes(2);
  });

  it("opens System Settings through the desktop bridge", () => {
    const openFullDiskAccessSettings = vi.fn(async () => {});
    installDesktopAPI({
      getFullDiskAccess: async () => ({ status: "missing", appName: "x" }),
      openFullDiskAccessSettings,
    });
    const { result } = renderHook(() => useFullDiskAccess());
    result.current.openSettings();
    expect(openFullDiskAccessSettings).toHaveBeenCalledOnce();
  });
});
