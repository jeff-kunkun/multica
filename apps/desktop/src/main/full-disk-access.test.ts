import { describe, expect, it, vi } from "vitest";

vi.mock("electron", () => ({ app: {}, ipcMain: {}, shell: {} }));

import { probeFullDiskAccess } from "./full-disk-access";

function errno(code: string): NodeJS.ErrnoException {
  return Object.assign(new Error(code), { code });
}

const handle = { close: async () => {} };

describe("probeFullDiskAccess", () => {
  it("is unsupported off macOS without touching the disk", async () => {
    const openFile = vi.fn();
    expect(await probeFullDiskAccess("linux", "/home/u", openFile)).toBe(
      "unsupported",
    );
    expect(openFile).not.toHaveBeenCalled();
  });

  it("reports granted when a protected file opens", async () => {
    expect(
      await probeFullDiskAccess("darwin", "/Users/u", async () => handle),
    ).toBe("granted");
  });

  it("reports missing when macOS refuses the read", async () => {
    const openFile = async () => {
      throw errno("EPERM");
    };
    expect(await probeFullDiskAccess("darwin", "/Users/u", openFile)).toBe(
      "missing",
    );
  });

  it("skips probes that don't exist on this Mac", async () => {
    const seen: string[] = [];
    const openFile = async (path: string) => {
      seen.push(path);
      if (path.endsWith("TCC.db")) throw errno("ENOENT");
      throw errno("EPERM");
    };
    expect(await probeFullDiskAccess("darwin", "/Users/u", openFile)).toBe(
      "missing",
    );
    expect(seen).toHaveLength(2);
  });

  it("stays unsupported when no probe exists", async () => {
    const openFile = async () => {
      throw errno("ENOENT");
    };
    expect(await probeFullDiskAccess("darwin", "/Users/u", openFile)).toBe(
      "unsupported",
    );
  });
});
