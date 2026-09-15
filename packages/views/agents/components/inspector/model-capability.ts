import type { RuntimeModel } from "@multica/core/types";

// Claude Code appends a context-window modifier to some runtime-native model
// IDs (for example, claude-opus-5[1m]). Restrict inheritance to a numeric
// context size so arbitrary bracketed variants remain fail-closed.
const CLAUDE_CONTEXT_WINDOW_TAG = /\[[1-9]\d*[km]\]$/;

// Product-name spellings DSH does not list, mapped onto the suffix it does.
// Capability lookup only — never rewrite the persisted agent.model.
// deepseek-v4.1-flash: official API id is deepseek-flash; dsh 0.1.5-rc.1
// advertises deepseek-official/deepseek-flash labelled DeepSeek-V41-Flash.
const DSH_MODEL_SUFFIX_ALIASES: Record<string, string> = {
  "deepseek-v4.1-flash": "deepseek-flash",
};

export function modelIdForCapabilityLookup(
  provider: string,
  model: string,
): string {
  if (provider === "claude") {
    return model.replace(CLAUDE_CONTEXT_WINDOW_TAG, "");
  }
  if (provider === "dsh") {
    return decodeDshModelId(model);
  }
  return model;
}

// DSH advertises ids as encodeURIComponent(provider)+"/"+encodeURIComponent(model).
// Decoding each slash-separated segment lets an encoded catalog id match a
// decoded agent.model (and the reverse) so the thinking-level picker can
// resolve the entry instead of hiding.
function decodeDshModelId(model: string): string {
  return model
    .split("/")
    .map((part) => {
      try {
        return decodeURIComponent(part);
      } catch {
        return part;
      }
    })
    .join("/");
}

function normalizeModelToken(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, "");
}

function splitDshProviderModel(
  model: string,
): { prefix: string; suffix: string } | undefined {
  const decoded = decodeDshModelId(model);
  const slash = decoded.indexOf("/");
  if (slash <= 0 || slash === decoded.length - 1) return undefined;
  return {
    prefix: decoded.slice(0, slash),
    suffix: decoded.slice(slash + 1),
  };
}

function thinkingValuesSignature(
  thinking: RuntimeModel["thinking"] | undefined,
): string {
  const levels = thinking?.supported_levels ?? [];
  if (levels.length === 0) return "";
  return levels.map((level) => level.value).join("\0");
}

function findDshRecoverableCatalogEntry(
  models: readonly RuntimeModel[],
  model: string,
): RuntimeModel | undefined {
  const parts = splitDshProviderModel(model);
  if (!parts) return undefined;

  const suffixKey = parts.suffix.toLowerCase();
  const candidates = new Set<string>([suffixKey]);
  const alias = DSH_MODEL_SUFFIX_ALIASES[suffixKey];
  if (alias) candidates.add(alias.toLowerCase());
  const suffixNorm = normalizeModelToken(parts.suffix);

  const uniqueMatches = models.filter((entry) => {
    const entryParts = splitDshProviderModel(entry.id);
    if (!entryParts || entryParts.prefix !== parts.prefix) return false;
    if (candidates.has(entryParts.suffix.toLowerCase())) return true;
    return (
      normalizeModelToken(entryParts.suffix) === suffixNorm ||
      normalizeModelToken(entry.label) === suffixNorm
    );
  });
  if (uniqueMatches.length === 1) return uniqueMatches[0];

  // Same-prefix identical thinking is a recoverable path for a stale id.
  // Require two or more rows so this is not "borrow the first/only model".
  const prefixed = models.filter((entry) => {
    const entryParts = splitDshProviderModel(entry.id);
    return entryParts?.prefix === parts.prefix;
  });
  if (prefixed.length < 2) return undefined;
  const signature = thinkingValuesSignature(prefixed[0]?.thinking);
  if (!signature) return undefined;
  for (const entry of prefixed) {
    if (thinkingValuesSignature(entry.thinking) !== signature) {
      return undefined;
    }
  }
  return {
    id: model,
    label: model,
    thinking: prefixed[0]?.thinking,
  };
}

/**
 * Resolves the catalog entry used for capability display and cleanup. The raw
 * model remains the value persisted and sent to the runtime; only this lookup
 * identity is normalized.
 */
export function findModelCapabilityEntry(
  models: readonly RuntimeModel[],
  model: string,
  provider: string,
): RuntimeModel | undefined {
  if (!model) return undefined;
  const lookupId = modelIdForCapabilityLookup(provider, model);
  // Normalize the catalog side too. Claude discovery reports what the CLI
  // would really run, tag included (`claude-opus-5[1m]`), so comparing a
  // stripped query against raw catalog ids would miss the entry for the very
  // model the user just picked — and hiding a model's own effort picker is how
  // that failure would show up (MUL-6961). Mirrors the daemon's lookup in
  // ValidateThinkingLevelWith.
  const exact = models.find(
    (entry) => modelIdForCapabilityLookup(provider, entry.id) === lookupId,
  );
  if (exact) return exact;
  if (provider === "dsh") {
    return findDshRecoverableCatalogEntry(models, model);
  }
  return undefined;
}
