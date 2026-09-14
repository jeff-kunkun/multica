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
});
