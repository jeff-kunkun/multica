// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  TRANSFER_EXPORT_COMPLETED_KEY,
  hasCompletedTransferExport,
  markTransferExportCompleted,
  transferExportSourceHost,
  transferProgressRatio,
  type TransferProgressEvent,
} from "./workspace-transfer";

/** Canonical matrix for the export progress bar's denominator (DENE-240). */
describe("transferProgressRatio", () => {
  function sample(over: Partial<TransferProgressEvent>): TransferProgressEvent {
    return { phase: "running", ...over };
  }

  it("has no ratio while no counter carries a total", () => {
    expect(transferProgressRatio(sample({}))).toBeNull();
    expect(transferProgressRatio(sample({ phase: "estimating" }))).toBeNull();
    // A count with no denominator is exactly the case the bar cannot draw:
    // an export discovers attachments per message, so there is no honest total.
    expect(transferProgressRatio(sample({ attachmentsDownloaded: 12 }))).toBeNull();
    expect(transferProgressRatio(sample({ sessionsTotal: 0, sessionsDone: 0 }))).toBeNull();
  });

  it("tracks the task walk over the earlier stages once it reports a total", () => {
    // The task walk runs last and longest, so a sample carrying both must not
    // leave the bar pinned at the conversation group's finished 26 / 26.
    expect(
      transferProgressRatio(
        sample({ sessionsDone: 26, sessionsTotal: 26, issuesDone: 10, issuesTotal: 40 }),
      ),
    ).toBe(0.25);
  });

  it("falls back to sessions, then attachments", () => {
    expect(transferProgressRatio(sample({ sessionsDone: 13, sessionsTotal: 26 }))).toBe(0.5);
    expect(
      transferProgressRatio(sample({ attachmentsDownloaded: 3, attachmentsTotal: 12 })),
    ).toBe(0.25);
  });

  it("reads a missing done counter as zero and clamps an overshoot", () => {
    expect(transferProgressRatio(sample({ issuesTotal: 40 }))).toBe(0);
    // The source can grow during the walk, so done > total is reachable.
    expect(transferProgressRatio(sample({ issuesDone: 45, issuesTotal: 40 }))).toBe(1);
  });
});

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
