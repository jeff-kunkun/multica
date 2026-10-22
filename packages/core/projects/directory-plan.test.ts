import { describe, expect, it } from "vitest";
import { ApiError } from "../api/client";
import {
  provisionedDirectoryShouldBeRemoved,
  sanitizeProjectDirectoryName,
} from "./directory-plan";

describe("sanitizeProjectDirectoryName", () => {
  it("keeps a Chinese title as one path segment", () => {
    expect(sanitizeProjectDirectoryName("通力电梯图像追溯")).toBe("通力电梯图像追溯");
  });

  it("refuses a name that would leave the root", () => {
    expect(sanitizeProjectDirectoryName("../secret")).toBe(".. secret");
    expect(sanitizeProjectDirectoryName("..")).toBe("");
    expect(sanitizeProjectDirectoryName("a/b")).toBe("a b");
  });
});

describe("provisionedDirectoryShouldBeRemoved", () => {
  it("removes the directory when the server rolled the project back", () => {
    const err = new ApiError("no", 500, "error", { code: "rolled_back" });
    expect(provisionedDirectoryShouldBeRemoved(err)).toBe(true);
  });

  it("removes the directory on a refusal that committed nothing", () => {
    const err = new ApiError("upgrade the daemon", 422, "error");
    expect(provisionedDirectoryShouldBeRemoved(err)).toBe(true);
  });

  it("keeps the directory once the project has committed", () => {
    const err = new ApiError("failed to complete", 500, "error", {
      code: "committed",
    });
    expect(provisionedDirectoryShouldBeRemoved(err)).toBe(false);
    expect(provisionedDirectoryShouldBeRemoved(new Error("network"))).toBe(false);
  });
});
