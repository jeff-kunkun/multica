// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  TRANSFER_EXPORT_COMPLETED_KEY,
  hasCompletedTransferExport,
  markTransferExportCompleted,
  transferExportSourceHost,
} from "./workspace-transfer";

describe("transferExportSourceHost", () => {
  it("returns the host from an API base URL", () => {
    expect(transferExportSourceHost("https://api.multica.ai")).toBe(
      "api.multica.ai",
    );
    expect(transferExportSourceHost("https://ai.ferryway.cc/")).toBe(
      "ai.ferryway.cc",
    );
    expect(transferExportSourceHost("http://localhost:8080")).toBe(
      "localhost:8080",
    );
  });

  it("accepts a bare host and returns empty for a blank value", () => {
    expect(transferExportSourceHost("")).toBe("");
    expect(transferExportSourceHost("  api.multica.ai  ")).toBe(
      "api.multica.ai",
    );
  });
});

describe("transfer export completed flag", () => {
  it("is unset until markTransferExportCompleted writes the key", () => {
    const store = new Map<string, string>();
    const storage = {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => {
        store.set(key, value);
      },
    };

    expect(hasCompletedTransferExport(storage)).toBe(false);
    markTransferExportCompleted(storage);
    expect(store.get(TRANSFER_EXPORT_COMPLETED_KEY)).toBe("1");
    expect(hasCompletedTransferExport(storage)).toBe(true);
  });
});
