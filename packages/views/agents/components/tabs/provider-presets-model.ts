// Pure logic behind the agent accounts tab's "providers" section (DENE-348).
//
// A provider preset is one route the agent CLI can send a request down: an
// endpoint, the protocol it speaks, the credential that authenticates it and
// the models it offers. The daemon owns the files; this module owns the form
// rules, the view states and the shape of the body the daemon is handed.
//
// The one invariant that shapes everything here: THE KEY IS WRITE-ONLY. No
// read type in this file has a field for a key value, the form always starts
// with an empty key box (never the mask), and a blank box means "leave the
// stored credential alone" — never "clear it". Pre-filling the mask would make
// the next submit write `sk-…0000` itself as the new credential, which is the
// one mistake this surface cannot recover from: the real key is gone and
// nothing on screen would say so.
//
// Canonical tests: `provider-presets-model.test.ts`. The component suite keeps
// the happy path and the wiring only.

import {
  PROVIDER_PRESET_APIS,
  PROVIDER_PRESET_DEFAULT_API,
} from "@multica/core/runtimes";
import type {
  RuntimeProviderPreset,
  RuntimeProviderPresetModel,
  RuntimeProviderPresetUpsertInput,
} from "@multica/core/types";

/**
 * Runtime providers whose CLI this section can configure.
 *
 * One entry today. The daemon rejects an id it has no driver for rather than
 * guessing (`server/internal/daemon/dsh_providers.go`), so a second CLI is a
 * backend change that arrives with its own UI — this set follows it, it does
 * not predict it.
 */
const PRESET_CAPABLE_PROVIDERS = new Set(["dsh"]);

/**
 * Whether the runtime behind this agent has a provider-preset driver.
 *
 * The section is not rendered at all for anything else, rather than rendered
 * disabled: a machine with no driver has nothing to show and no action to
 * offer, and a greyed-out block would read as "not set up yet".
 */
export function supportsProviderPresets(provider: string | undefined): boolean {
  return PRESET_CAPABLE_PROVIDERS.has((provider ?? "").trim().toLowerCase());
}

export type ProviderPresetsViewState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "empty" }
  | { kind: "ready"; presets: RuntimeProviderPreset[] };

export interface ProviderPresetsViewInput {
  presets: RuntimeProviderPreset[] | undefined;
  loading: boolean;
  error: string;
}

/**
 * The four states the section can be in, in the order they preempt each other.
 *
 * `error` outranks having stale rows on purpose: the list drives an activate
 * and a delete, and acting on a list we could not refresh would target a
 * preset the machine may no longer have. An untrustworthy list therefore
 * offers no editing affordance at all — the same rule the account half of this
 * tab follows (DENE-305).
 */
export function providerPresetsViewState(
  input: ProviderPresetsViewInput,
): ProviderPresetsViewState {
  if (input.error) return { kind: "error", message: input.error };
  if (input.loading) return { kind: "loading" };
  const presets = input.presets ?? [];
  if (presets.length === 0) return { kind: "empty" };
  return { kind: "ready", presets };
}

/** Whether the section may offer add / edit / activate / delete. */
export function canManageProviderPresets(
  state: ProviderPresetsViewState,
): boolean {
  return state.kind === "ready" || state.kind === "empty";
}

/** One model row being edited. Kept as strings — it is form state. */
export interface ProviderPresetModelDraft {
  id: string;
  name: string;
}

/**
 * The form's state.
 *
 * `apiKey` is write-only and therefore always starts empty; `hasKey` and
 * `keyMask` are the read-side facts about the STORED credential and are
 * display-only. Keeping them in three separate fields is what makes it
 * impossible to accidentally submit the mask: nothing ever assigns `keyMask`
 * into `apiKey`.
 */
export interface ProviderPresetForm {
  /** Empty when adding; the preset's id when editing (the id is its identity). */
  editingId: string;
  id: string;
  baseUrl: string;
  api: string;
  apiKeyEnv: string;
  apiKey: string;
  hasKey: boolean;
  keyMask: string;
  models: ProviderPresetModelDraft[];
}

/** A blank form for "add a provider". */
export function emptyProviderPresetForm(): ProviderPresetForm {
  return {
    editingId: "",
    id: "",
    baseUrl: "",
    api: PROVIDER_PRESET_DEFAULT_API,
    apiKeyEnv: "",
    apiKey: "",
    hasKey: false,
    keyMask: "",
    models: [{ id: "", name: "" }],
  };
}

/**
 * Fill the form from an existing preset.
 *
 * Every field is carried over except the credential: `apiKey` is hardcoded to
 * `""` here and there is no branch that can change that. See the file header
 * for why.
 */
export function providerPresetFormFrom(
  preset: RuntimeProviderPreset,
): ProviderPresetForm {
  return {
    editingId: preset.id,
    id: preset.id,
    baseUrl: preset.base_url ?? "",
    api: knownPresetApi(preset.api),
    apiKeyEnv: preset.api_key_env ?? "",
    apiKey: "",
    hasKey: preset.has_key === true,
    keyMask: preset.key_mask ?? "",
    models:
      preset.models.length > 0
        ? preset.models.map((model) => ({ id: model.id, name: model.name ?? "" }))
        : [{ id: "", name: "" }],
  };
}

/**
 * Narrow a reported protocol to one the select can show.
 *
 * A daemon newer than this build may report a protocol this list does not
 * have. Falling back to the default keeps the select controlled, and the
 * validator below refuses a protocol it does not know — so an unknown value
 * cannot be silently written back over the real one.
 */
function knownPresetApi(api: string | undefined): string {
  const value = (api ?? "").trim();
  return (PROVIDER_PRESET_APIS as readonly string[]).includes(value)
    ? value
    : PROVIDER_PRESET_DEFAULT_API;
}

/** Which fields a submit would reject, as translation-key suffixes. */
export type ProviderPresetFieldError =
  | "id_required"
  | "id_invalid"
  | "base_url_required"
  | "base_url_invalid"
  | "api_invalid"
  | "api_key_env_invalid"
  | "models_required";

/**
 * A preset id becomes a YAML mapping key under `llm-pi-ai.providers` and is
 * written verbatim into `agent-default-model.provider`, so it is restricted to
 * characters that need no quoting and cannot traverse into another node.
 */
const PRESET_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/** Env var names are `[A-Z_][A-Z0-9_]*` — the shell's own rule, uppercased. */
const ENV_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function validateProviderPresetForm(
  form: ProviderPresetForm,
): ProviderPresetFieldError[] {
  const errors: ProviderPresetFieldError[] = [];

  const id = form.id.trim();
  if (!id) errors.push("id_required");
  else if (!PRESET_ID_PATTERN.test(id)) errors.push("id_invalid");

  const baseUrl = form.baseUrl.trim();
  if (!baseUrl) errors.push("base_url_required");
  else if (!isHttpUrl(baseUrl)) errors.push("base_url_invalid");

  if (!(PROVIDER_PRESET_APIS as readonly string[]).includes(form.api.trim())) {
    errors.push("api_invalid");
  }

  const env = form.apiKeyEnv.trim();
  // Empty is valid and means "let the daemon derive one from the id".
  if (env && !ENV_NAME_PATTERN.test(env)) errors.push("api_key_env_invalid");

  if (providerPresetModels(form).length === 0) errors.push("models_required");

  return errors;
}

/**
 * `http`/`https` only.
 *
 * Not a cosmetic check: this value decides where the stored key is sent. A
 * `file:` or `data:` endpoint is not something the CLI can call, and letting
 * one through would write a route that fails at request time instead of at the
 * form.
 */
function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

/** The model rows that survive submission — blank rows are scaffolding. */
export function providerPresetModels(
  form: ProviderPresetForm,
): RuntimeProviderPresetModel[] {
  return form.models
    .map((model) => ({ id: model.id.trim(), name: model.name.trim() }))
    .filter((model) => model.id !== "")
    .map((model) => (model.name ? model : { id: model.id }));
}

/**
 * The upsert body.
 *
 * `api_key` is present ONLY when the user typed one. A blank box produces no
 * field at all, which the daemon reads as "keep the stored credential" — the
 * behaviour a user editing an endpoint without retyping their key expects.
 */
export function providerPresetUpsertInput(
  form: ProviderPresetForm,
): RuntimeProviderPresetUpsertInput {
  const input: RuntimeProviderPresetUpsertInput = {
    id: form.id.trim(),
    api: form.api.trim(),
    base_url: form.baseUrl.trim(),
    api_key_env: form.apiKeyEnv.trim(),
    models: providerPresetModels(form),
  };
  const typed = form.apiKey.trim();
  if (typed) input.api_key = typed;
  return input;
}

/**
 * What the credential line says about a preset. Three answers, because
 * "stored, and here is its mask" and "stored, but this daemon reported no
 * mask" are the same fact and must not read as "nothing stored".
 */
export type ProviderPresetKeyState = "masked" | "stored" | "absent";

export function providerPresetKeyState(
  preset: RuntimeProviderPreset,
): ProviderPresetKeyState {
  if (preset.has_key !== true) return "absent";
  return preset.key_mask ? "masked" : "stored";
}

/** `POST https://host/v1 · 2 models`-style secondary line, protocol first. */
export function providerPresetSummaryLine(preset: RuntimeProviderPreset): string {
  const parts = [preset.base_url ?? "", preset.api ?? ""].filter(Boolean);
  return parts.join(" · ");
}

/**
 * Whether deleting this preset needs the "you will have no default model"
 * confirmation rather than the ordinary one.
 */
export function deletingActivePreset(preset: RuntimeProviderPreset): boolean {
  return preset.active === true;
}
