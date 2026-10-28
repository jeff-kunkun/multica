import { afterEach, describe, expect, it } from "vitest";
import { configStore, featureFlagEnabled, parseFeatureFlagList } from "./index";

afterEach(() => {
  configStore.getState().setFeatureFlags({});
  configStore.getState().setLocalFeatureFlags({});
});

describe("parseFeatureFlagList", () => {
  it("turns listed flags on and dash-prefixed flags off", () => {
    expect(parseFeatureFlagList(" a, -b ,,c ")).toEqual({ a: true, b: false, c: true });
    expect(parseFeatureFlagList(undefined)).toEqual({});
    expect(parseFeatureFlagList("-")).toEqual({});
  });
});

describe("local feature flags", () => {
  it("are kept apart from the server's flags, which replace only their own", () => {
    configStore.getState().setLocalFeatureFlags({ office: true });
    configStore.getState().setFeatureFlags({ other: true });
    const state = configStore.getState();
    expect(state.localFeatureFlags).toEqual({ office: true });
    expect(featureFlagEnabled(state.featureFlags, "office")).toBe(false);
  });
});
