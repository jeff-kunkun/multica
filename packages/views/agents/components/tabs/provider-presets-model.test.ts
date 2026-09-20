// @vitest-environment node

// Canonical suite for the providers section's rules. The component test
// (`agent-provider-presets-section.test.tsx`) keeps the happy path and the
// wiring and does not re-run this matrix through a DOM mount.

import { describe, expect, it } from "vitest";
import type { RuntimeProviderPreset } from "@multica/core/types";
import {
  canManageProviderPresets,
  deletingActivePreset,
  emptyProviderPresetForm,
  providerPresetFormFrom,
  providerPresetKeyState,
  providerPresetModels,
  providerPresetSummaryLine,
  providerPresetUpsertInput,
  providerPresetsViewState,
  supportsProviderPresets,
  validateProviderPresetForm,
} from "./provider-presets-model";

function preset(overrides: Partial<RuntimeProviderPreset> = {}): RuntimeProviderPreset {
  return {
    id: "command-code",
    api: "openai-completions",
    base_url: "https://api.example.test/v1",
    api_key_env: "COMMAND_CODE_API_KEY",
    key_mask: "sk-…0000",
    has_key: true,
    models: [{ id: "m1", name: "Model One" }],
    ...overrides,
  };
}

describe("supportsProviderPresets", () => {
  it("accepts dsh regardless of casing or padding", () => {
    expect(supportsProviderPresets("dsh")).toBe(true);
    expect(supportsProviderPresets(" DSH ")).toBe(true);
  });

  it("rejects every provider without a daemon driver", () => {
    for (const provider of ["antigravity", "claude", "codex", "", undefined]) {
      expect(supportsProviderPresets(provider)).toBe(false);
    }
  });
});

describe("providerPresetsViewState", () => {
  it("reports an unreadable list as an error even when rows are present", () => {
    // Stale rows drive an activate and a delete. Rendering them as usable
    // would let the user act on a preset the machine may no longer have.
    const state = providerPresetsViewState({
      presets: [preset()],
      loading: false,
      error: "daemon did not respond within 30 seconds",
    });
    expect(state.kind).toBe("error");
    expect(canManageProviderPresets(state)).toBe(false);
  });

  it("is loading before the first answer", () => {
    expect(
      providerPresetsViewState({ presets: undefined, loading: true, error: "" }).kind,
    ).toBe("loading");
    expect(
      canManageProviderPresets(
        providerPresetsViewState({ presets: undefined, loading: true, error: "" }),
      ),
    ).toBe(false);
  });

  it("offers the add affordance on an empty but trustworthy list", () => {
    const state = providerPresetsViewState({ presets: [], loading: false, error: "" });
    expect(state.kind).toBe("empty");
    expect(canManageProviderPresets(state)).toBe(true);
  });

  it("is ready with rows", () => {
    const state = providerPresetsViewState({
      presets: [preset()],
      loading: false,
      error: "",
    });
    expect(state).toEqual({ kind: "ready", presets: [preset()] });
    expect(canManageProviderPresets(state)).toBe(true);
  });
});

describe("providerPresetFormFrom", () => {
  it("never carries a credential into the form", () => {
    const form = providerPresetFormFrom(preset());
    expect(form.apiKey).toBe("");
    // The mask is kept as a display fact, strictly apart from the input value:
    // pre-filling it would make the next submit write "sk-…0000" as the key.
    expect(form.keyMask).toBe("sk-…0000");
    expect(form.hasKey).toBe(true);
  });

  it("falls back to the default protocol when the daemon reports an unknown one", () => {
    expect(providerPresetFormFrom(preset({ api: "grpc-whatever" })).api).toBe(
      "openai-completions",
    );
  });

  it("gives a preset with no models one blank row to fill", () => {
    expect(providerPresetFormFrom(preset({ models: [] })).models).toEqual([
      { id: "", name: "" },
    ]);
  });
});

describe("validateProviderPresetForm", () => {
  function valid() {
    return {
      ...emptyProviderPresetForm(),
      id: "my-provider",
      baseUrl: "https://api.example.test/v1",
      models: [{ id: "m1", name: "" }],
    };
  }

  it("accepts a minimal complete form", () => {
    expect(validateProviderPresetForm(valid())).toEqual([]);
  });

  it("requires an id and refuses one that would need YAML quoting", () => {
    expect(validateProviderPresetForm({ ...valid(), id: "  " })).toContain("id_required");
    for (const bad of ["my provider", "a/b", "-lead", "a:b"]) {
      expect(validateProviderPresetForm({ ...valid(), id: bad })).toContain("id_invalid");
    }
  });

  it("requires an http(s) endpoint", () => {
    expect(validateProviderPresetForm({ ...valid(), baseUrl: "" })).toContain(
      "base_url_required",
    );
    // Where the stored key gets sent — a scheme the CLI cannot call is a
    // broken route, and the form is the right place to say so.
    for (const bad of ["ftp://x/y", "file:///etc/passwd", "not a url"]) {
      expect(validateProviderPresetForm({ ...valid(), baseUrl: bad })).toContain(
        "base_url_invalid",
      );
    }
  });

  it("refuses a protocol the daemon driver does not list", () => {
    expect(validateProviderPresetForm({ ...valid(), api: "grpc" })).toContain(
      "api_invalid",
    );
  });

  it("treats a blank env name as valid (the daemon derives one)", () => {
    expect(validateProviderPresetForm({ ...valid(), apiKeyEnv: "" })).toEqual([]);
  });

  it("refuses an env name the shell could not export", () => {
    expect(validateProviderPresetForm({ ...valid(), apiKeyEnv: "9LIVES" })).toContain(
      "api_key_env_invalid",
    );
    expect(validateProviderPresetForm({ ...valid(), apiKeyEnv: "MY KEY" })).toContain(
      "api_key_env_invalid",
    );
  });

  it("requires at least one model, because saving verifies one against the endpoint", () => {
    expect(
      validateProviderPresetForm({ ...valid(), models: [{ id: "  ", name: "x" }] }),
    ).toContain("models_required");
    expect(validateProviderPresetForm({ ...valid(), models: [] })).toContain(
      "models_required",
    );
    expect(
      validateProviderPresetForm({ ...valid(), models: [{ id: "m1", name: "" }] }),
    ).not.toContain("models_required");
  });
});

describe("providerPresetModels", () => {
  it("drops blank scaffolding rows and trims the rest", () => {
    const form = {
      ...emptyProviderPresetForm(),
      models: [
        { id: " m1 ", name: " Model One " },
        { id: "", name: "" },
        { id: "m2", name: "" },
      ],
    };
    expect(providerPresetModels(form)).toEqual([
      { id: "m1", name: "Model One" },
      { id: "m2" },
    ]);
  });
});

describe("providerPresetUpsertInput", () => {
  function form() {
    return {
      ...emptyProviderPresetForm(),
      editingId: "command-code",
      id: "command-code",
      baseUrl: "https://api.example.test/v1",
      hasKey: true,
      keyMask: "sk-…0000",
      models: [{ id: "m1", name: "" }],
    };
  }

  it("omits api_key when the box was left blank, so the stored key survives", () => {
    const input = providerPresetUpsertInput(form());
    expect("api_key" in input).toBe(false);
  });

  it("omits api_key when the box holds only whitespace", () => {
    const input = providerPresetUpsertInput({ ...form(), apiKey: "   " });
    expect("api_key" in input).toBe(false);
  });

  it("never lets the mask reach the wire as a credential", () => {
    // Belt and braces for the invariant this whole section is built around:
    // whatever the mask is, it lives in `keyMask` and nothing copies it into
    // `apiKey`, so a submit made without typing carries no key at all.
    const input = providerPresetUpsertInput(form());
    expect(JSON.stringify(input)).not.toContain("sk-…0000");
  });

  it("sends a typed key, trimmed", () => {
    expect(providerPresetUpsertInput({ ...form(), apiKey: " sk-test-0000 " }).api_key).toBe(
      "sk-test-0000",
    );
  });
});

describe("providerPresetKeyState", () => {
  it("is masked when the daemon reported both the bit and the mask", () => {
    expect(providerPresetKeyState(preset())).toBe("masked");
  });

  it("is stored — not absent — when a key exists without a mask", () => {
    expect(providerPresetKeyState(preset({ key_mask: "" }))).toBe("stored");
  });

  it("is absent only when has_key is strictly false", () => {
    expect(providerPresetKeyState(preset({ has_key: false }))).toBe("absent");
    expect(
      providerPresetKeyState(preset({ has_key: undefined as never, key_mask: "sk-…0" })),
    ).toBe("absent");
  });
});

describe("presentation helpers", () => {
  it("summarises endpoint and protocol, skipping what the daemon omitted", () => {
    expect(providerPresetSummaryLine(preset())).toBe(
      "https://api.example.test/v1 · openai-completions",
    );
    expect(providerPresetSummaryLine(preset({ api: undefined }))).toBe(
      "https://api.example.test/v1",
    );
  });

  it("flags a delete that would leave the machine with no default model", () => {
    expect(deletingActivePreset(preset({ active: true }))).toBe(true);
    expect(deletingActivePreset(preset())).toBe(false);
  });
});
