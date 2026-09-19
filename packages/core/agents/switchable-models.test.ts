// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent, AgentSwitchableModel } from "../types";
import {
  AGENT_SWITCHABLE_MODELS_MAX,
  AGENT_SWITCHABLE_MODEL_ID_MAX_LENGTH,
  AGENT_SWITCHABLE_MODEL_NOTE_MAX_LENGTH,
  isAgentSwitchable,
  normaliseSwitchableModelsDraft,
  selectAgentSwitchableModels,
  switchableModelsEqual,
} from "./switchable-models";

function agent(value: unknown): Pick<Agent, "switchable_models"> {
  return { switchable_models: value } as Pick<Agent, "switchable_models">;
}

describe("selectAgentSwitchableModels", () => {
  it("reads a well-formed lineup unchanged", () => {
    expect(
      selectAgentSwitchableModels(
        agent([
          { model: "claude-opus-5[1m]", role: "default", note: "" },
          { model: "claude-opus-5", role: "fallback", note: "降级" },
        ]),
      ),
    ).toEqual([
      { model: "claude-opus-5[1m]", role: "default", note: "" },
      { model: "claude-opus-5", role: "fallback", note: "降级" },
    ]);
  });

  it("treats a missing field as no lineup", () => {
    expect(selectAgentSwitchableModels(agent(undefined))).toEqual([]);
  });

  it("treats a non-array payload as no lineup", () => {
    expect(selectAgentSwitchableModels(agent("claude-opus-5"))).toEqual([]);
    expect(selectAgentSwitchableModels(agent({ model: "x" }))).toEqual([]);
    expect(selectAgentSwitchableModels(agent(null))).toEqual([]);
  });

  it("drops entries without a usable model id", () => {
    expect(
      selectAgentSwitchableModels(
        agent([
          null,
          "claude-opus-5",
          { role: "default", note: "" },
          { model: "   ", role: "default", note: "" },
          { model: "  kept  ", role: "default", note: "" },
        ]),
      ),
    ).toEqual([{ model: "kept", role: "default", note: "" }]);
  });

  it("buckets an unknown role as fallback and a missing note as empty", () => {
    expect(
      selectAgentSwitchableModels(
        agent([{ model: "m", role: "premium" }, { model: "n", role: 7 }]),
      ),
    ).toEqual([
      { model: "m", role: "fallback", note: "" },
      { model: "n", role: "fallback", note: "" },
    ]);
  });
});

describe("isAgentSwitchable", () => {
  it("is false for an empty, missing, or all-junk lineup", () => {
    expect(isAgentSwitchable(agent([]))).toBe(false);
    expect(isAgentSwitchable(agent(undefined))).toBe(false);
    expect(isAgentSwitchable(agent([{ model: "", role: "default", note: "" }]))).toBe(
      false,
    );
  });

  it("is true once one entry carries a model", () => {
    expect(
      isAgentSwitchable(agent([{ model: "m", role: "default", note: "" }])),
    ).toBe(true);
  });
});

describe("normaliseSwitchableModelsDraft", () => {
  it("trims model and note", () => {
    expect(
      normaliseSwitchableModelsDraft([
        { model: "  m  ", role: "default", note: "  hi  " },
      ]),
    ).toEqual([{ model: "m", role: "default", note: "hi" }]);
  });

  it("drops rows the user has not named a model for yet", () => {
    expect(
      normaliseSwitchableModelsDraft([
        { model: "m", role: "default", note: "" },
        { model: "", role: "fallback", note: "note without a model" },
        { model: "   ", role: "batch", note: "" },
      ]),
    ).toEqual([{ model: "m", role: "default", note: "" }]);
  });

  it("returns an empty lineup for an all-blank draft — the single-model write", () => {
    expect(
      normaliseSwitchableModelsDraft([{ model: "", role: "default", note: "" }]),
    ).toEqual([]);
    expect(normaliseSwitchableModelsDraft([])).toEqual([]);
  });

  it("clamps to the server's row, id and note limits", () => {
    const rows: AgentSwitchableModel[] = Array.from(
      { length: AGENT_SWITCHABLE_MODELS_MAX + 5 },
      (_, index) => ({
        model: `m${index}`,
        role: "fallback" as const,
        note: "",
      }),
    );
    expect(normaliseSwitchableModelsDraft(rows)).toHaveLength(
      AGENT_SWITCHABLE_MODELS_MAX,
    );

    const long = normaliseSwitchableModelsDraft([
      { model: "m".repeat(400), role: "default", note: "n".repeat(400) },
    ]);
    expect(long[0]?.model).toHaveLength(AGENT_SWITCHABLE_MODEL_ID_MAX_LENGTH);
    expect(long[0]?.note).toHaveLength(AGENT_SWITCHABLE_MODEL_NOTE_MAX_LENGTH);
  });

  it("buckets an unknown role as fallback", () => {
    expect(
      normaliseSwitchableModelsDraft([
        { model: "m", role: "premium" as never, note: "" },
      ]),
    ).toEqual([{ model: "m", role: "fallback", note: "" }]);
  });
});

describe("switchableModelsEqual", () => {
  it("compares by value, not identity", () => {
    const rows: AgentSwitchableModel[] = [
      { model: "m", role: "default", note: "n" },
    ];
    expect(switchableModelsEqual(rows, [{ ...rows[0]! }])).toBe(true);
    expect(switchableModelsEqual([], [])).toBe(true);
  });

  it("separates length, order and field changes", () => {
    const a: AgentSwitchableModel[] = [
      { model: "m1", role: "default", note: "" },
      { model: "m2", role: "fallback", note: "" },
    ];
    expect(switchableModelsEqual(a, [a[0]!])).toBe(false);
    expect(switchableModelsEqual(a, [a[1]!, a[0]!])).toBe(false);
    expect(
      switchableModelsEqual(a, [a[0]!, { ...a[1]!, note: "changed" }]),
    ).toBe(false);
    expect(
      switchableModelsEqual(a, [a[0]!, { ...a[1]!, role: "batch" }]),
    ).toBe(false);
  });
});
