import type { Agent, AgentSwitchableModel, AgentSwitchableModelRole } from "../types";

/**
 * Limits mirrored from the backend validator
 * (`normaliseAgentSwitchableModels` in `server/internal/handler/agent.go`) so
 * the editor refuses what the server would reject instead of round-tripping a
 * 400 the user has to decode.
 */
export const AGENT_SWITCHABLE_MODELS_MAX = 32;
export const AGENT_SWITCHABLE_MODEL_ID_MAX_LENGTH = 200;
export const AGENT_SWITCHABLE_MODEL_NOTE_MAX_LENGTH = 200;

export const AGENT_SWITCHABLE_MODEL_ROLES = [
  "default",
  "fallback",
  "batch",
] as const;

function isRole(value: unknown): value is AgentSwitchableModelRole {
  return (
    typeof value === "string" &&
    (AGENT_SWITCHABLE_MODEL_ROLES as readonly string[]).includes(value)
  );
}

/**
 * Defensive read of `Agent.switchable_models`. Older servers omit the field
 * entirely and a drifting backend may send entries missing `model` or with an
 * unknown `role`; both must read as "no lineup" rather than crash the header
 * or seed the editor with junk. An unknown role falls back to `fallback`,
 * which is the only role whose ordering is meaningful and therefore the safest
 * bucket for something we cannot classify.
 */
export function selectAgentSwitchableModels(
  agent: Pick<Agent, "switchable_models">,
): AgentSwitchableModel[] {
  const raw = agent.switchable_models;
  if (!Array.isArray(raw)) return [];
  const entries: AgentSwitchableModel[] = [];
  for (const item of raw) {
    if (item == null || typeof item !== "object") continue;
    const model = typeof item.model === "string" ? item.model.trim() : "";
    if (model.length === 0) continue;
    entries.push({
      model,
      role: isRole(item.role) ? item.role : "fallback",
      note: typeof item.note === "string" ? item.note : "",
    });
  }
  return entries;
}

/**
 * True when the agent runs as a lineup ("可换档") rather than a single model.
 * The header row and the inspector switch read the same predicate so they can
 * never disagree about which mode the agent is in.
 */
export function isAgentSwitchable(
  agent: Pick<Agent, "switchable_models">,
): boolean {
  return selectAgentSwitchableModels(agent).length > 0;
}

/**
 * Turns an editor draft into the payload for `PUT /api/agents/:id`. Rows whose
 * model is still blank are dropped rather than sent: the row exists because the
 * user just clicked "add", and the server would reject the whole request for
 * it. An empty result is the "single model" state and clears the lineup — that
 * is the write behind turning the switch off.
 */
export function normaliseSwitchableModelsDraft(
  rows: readonly AgentSwitchableModel[],
): AgentSwitchableModel[] {
  const normalised: AgentSwitchableModel[] = [];
  for (const row of rows) {
    const model = row.model.trim().slice(0, AGENT_SWITCHABLE_MODEL_ID_MAX_LENGTH);
    if (model.length === 0) continue;
    normalised.push({
      model,
      role: isRole(row.role) ? row.role : "fallback",
      note: row.note.trim().slice(0, AGENT_SWITCHABLE_MODEL_NOTE_MAX_LENGTH),
    });
    if (normalised.length === AGENT_SWITCHABLE_MODELS_MAX) break;
  }
  return normalised;
}

/** Deep equality for autosave: array identity changes on every keystroke. */
export function switchableModelsEqual(
  left: readonly AgentSwitchableModel[],
  right: readonly AgentSwitchableModel[],
): boolean {
  if (left.length !== right.length) return false;
  return left.every((row, index) => {
    const other = right[index];
    return (
      other != null &&
      row.model === other.model &&
      row.role === other.role &&
      row.note === other.note
    );
  });
}
