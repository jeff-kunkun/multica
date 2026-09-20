import { describe, expect, it } from "vitest";

import { parseRoutingModels } from "./routing-models";

describe("parseRoutingModels", () => {
  it("keeps usable ids, trims them, and removes duplicates", () => {
    expect(
      parseRoutingModels({ models: [" model-a ", "model-b", "model-a", ""] }),
    ).toEqual({ models: ["model-a", "model-b"] });
  });

  it("falls back to an empty catalog for malformed responses", () => {
    expect(parseRoutingModels({ models: ["model-a", 42] })).toEqual({ models: [] });
    expect(parseRoutingModels(null)).toEqual({ models: [] });
  });
});
