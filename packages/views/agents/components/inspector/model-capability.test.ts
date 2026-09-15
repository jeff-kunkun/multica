// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RuntimeModel } from "@multica/core/types";
import {
  findModelCapabilityEntry,
  modelIdForCapabilityLookup,
} from "./model-capability";

const CLAUDE_MODELS: RuntimeModel[] = [
  { id: "claude-opus-5", label: "Claude Opus 5" },
];

describe("model capability lookup", () => {
  it("inherits a base Claude model for valid context-window tags", () => {
    expect(modelIdForCapabilityLookup("claude", "claude-opus-5[1m]")).toBe(
      "claude-opus-5",
    );
    expect(modelIdForCapabilityLookup("claude", "claude-opus-5[500k]")).toBe(
      "claude-opus-5",
    );
    expect(
      findModelCapabilityEntry(
        CLAUDE_MODELS,
        "claude-opus-5[1m]",
        "claude",
      )?.id,
    ).toBe("claude-opus-5");
  });

  it("keeps malformed Claude tags and other providers exact", () => {
    expect(
      modelIdForCapabilityLookup("claude", "claude-opus-5[weird]"),
    ).toBe("claude-opus-5[weird]");
    expect(modelIdForCapabilityLookup("codex", "gpt-5.6-sol[1m]")).toBe(
      "gpt-5.6-sol[1m]",
    );
    expect(
      findModelCapabilityEntry(
        CLAUDE_MODELS,
        "claude-opus-5[weird]",
        "claude",
      ),
    ).toBeUndefined();
  });

  // MUL-6961: discovery reports what the CLI actually runs, so a catalog id can
  // now carry the tag itself. Both sides need normalizing — matching only the
  // query side would hide the effort picker for the model the user just picked.
  it("matches a tagged catalog entry from either spelling", () => {
    const TAGGED: RuntimeModel[] = [
      { id: "claude-opus-5[1m]", label: "Opus (1M context)", provider: "anthropic" },
      { id: "claude-sonnet-5", label: "Sonnet", provider: "anthropic" },
    ];
    expect(
      findModelCapabilityEntry(TAGGED, "claude-opus-5[1m]", "claude")?.id,
    ).toBe("claude-opus-5[1m]");
    // An agent pinned before discovery landed stores the untagged id, and must
    // still resolve to the tagged entry rather than losing its picker.
    expect(
      findModelCapabilityEntry(TAGGED, "claude-opus-5", "claude")?.id,
    ).toBe("claude-opus-5[1m]");
    // Untagged entries keep matching exactly.
    expect(
      findModelCapabilityEntry(TAGGED, "claude-sonnet-5", "claude")?.id,
    ).toBe("claude-sonnet-5");
  });

  it("matches DSH catalog ids whether the slash in the model id is encoded", () => {
    const DSH_MODELS: RuntimeModel[] = [
      {
        id: "deepseek-official/deepseek-v4%2Fflash",
        label: "DeepSeek V4 Flash",
        thinking: {
          supported_levels: [
            { value: "off", label: "Off" },
            { value: "high", label: "High" },
          ],
        },
      },
    ];
    expect(
      modelIdForCapabilityLookup(
        "dsh",
        "deepseek-official/deepseek-v4%2Fflash",
      ),
    ).toBe("deepseek-official/deepseek-v4/flash");
    expect(
      findModelCapabilityEntry(
        DSH_MODELS,
        "deepseek-official/deepseek-v4/flash",
        "dsh",
      )?.id,
    ).toBe("deepseek-official/deepseek-v4%2Fflash");
    expect(
      findModelCapabilityEntry(
        DSH_MODELS,
        "deepseek-official/deepseek-v4%2Fflash",
        "dsh",
      )?.thinking?.supported_levels.map((level) => level.value),
    ).toEqual(["off", "high"]);
  });

  const DSH_THINKING = {
    supported_levels: [
      { value: "off", label: "Off" },
      { value: "low", label: "Low" },
      { value: "high", label: "High" },
      { value: "max", label: "Max" },
    ],
    default_level: "high",
  };

  // Field catalog from dsh --profile multica --list-models on 0.1.5-rc.1,
  // with labels reduced to the model suffix as in the DENE-159 review repro.
  const FIELD_DSH_CATALOG: RuntimeModel[] = [
    "deepseek-v4-flash",
    "deepseek-v4-flash-vision-exp",
    "deepseek-v4-pro",
    "deepseek-flash",
  ].map((modelId) => ({
    id: `deepseek-official/${modelId}`,
    label: modelId,
    thinking: DSH_THINKING,
  }));

  it("maps the persisted DSH V4.1 Flash id onto the advertised deepseek-flash row", () => {
    expect(
      findModelCapabilityEntry(
        FIELD_DSH_CATALOG,
        "deepseek-official/deepseek-v4.1-flash",
        "dsh",
      )?.id,
    ).toBe("deepseek-official/deepseek-flash");
  });

  it("matches a stale DSH product-name spelling to the live catalog label", () => {
    const liveLabels: RuntimeModel[] = [
      {
        id: "deepseek-official/deepseek-v4-flash",
        label: "DeepSeek-V4-Flash",
        thinking: DSH_THINKING,
      },
      {
        id: "deepseek-official/deepseek-flash",
        label: "DeepSeek-V41-Flash",
        thinking: DSH_THINKING,
      },
    ];
    expect(
      findModelCapabilityEntry(
        liveLabels,
        "deepseek-official/deepseek-v4.1-flash",
        "dsh",
      )?.id,
    ).toBe("deepseek-official/deepseek-flash");
  });

  it("does not borrow the first DSH model's unique thinking levels", () => {
    const mixed: RuntimeModel[] = [
      {
        id: "deepseek-official/deepseek-v4-flash",
        label: "deepseek-v4-flash",
        thinking: {
          supported_levels: [
            { value: "off", label: "Off" },
            { value: "ultra", label: "Ultra" },
          ],
        },
      },
      {
        id: "deepseek-official/deepseek-v4-pro",
        label: "deepseek-v4-pro",
        thinking: {
          supported_levels: [{ value: "off", label: "Off" }],
        },
      },
    ];
    expect(
      findModelCapabilityEntry(
        mixed,
        "deepseek-official/deepseek-v4.1-flash",
        "dsh",
      ),
    ).toBeUndefined();
  });

  it("does not recover a stale DSH id from a single catalog row", () => {
    expect(
      findModelCapabilityEntry(
        [
          {
            id: "deepseek-official/deepseek-v4-flash",
            label: "deepseek-v4-flash",
            thinking: DSH_THINKING,
          },
        ],
        "deepseek-official/deepseek-v4.1-flash",
        "dsh",
      ),
    ).toBeUndefined();
  });

  it("does not recover a DSH id across provider prefixes", () => {
    expect(
      findModelCapabilityEntry(
        FIELD_DSH_CATALOG,
        "other-provider/deepseek-v4.1-flash",
        "dsh",
      ),
    ).toBeUndefined();
  });

  it("recovers identical same-prefix thinking without rewriting the persisted id", () => {
    const catalog: RuntimeModel[] = [
      {
        id: "deepseek-official/deepseek-v4-flash",
        label: "deepseek-v4-flash",
        thinking: DSH_THINKING,
      },
      {
        id: "deepseek-official/deepseek-v4-pro",
        label: "deepseek-v4-pro",
        thinking: DSH_THINKING,
      },
    ];
    const entry = findModelCapabilityEntry(
      catalog,
      "deepseek-official/stale-unknown-flash",
      "dsh",
    );
    expect(entry?.id).toBe("deepseek-official/stale-unknown-flash");
    expect(entry?.thinking?.supported_levels.map((level) => level.value)).toEqual(
      ["off", "low", "high", "max"],
    );
  });
});
