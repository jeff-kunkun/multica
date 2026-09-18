"use client";

import { useMemo, useState } from "react";
import { Loader2 } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import {
  baseRoleOptionsFor,
  canChangeBaseRole,
  isSpecialization,
} from "../specialization";
import { BaseRoleSelect, NO_BASE_ROLE } from "./base-role-select";

/**
 * The base role of an agent that ALREADY EXISTS.
 *
 * DENE-304 could only pick a base role while creating an agent, which left
 * every agent made before the feature — the whole reason it was asked for —
 * with no way into the tree short of deleting and re-creating it. The backend
 * always accepted the re-parent (`PUT /api/agents/{id}` with
 * `parent_agent_id`); only the affordance was missing.
 *
 * Detaching is deliberately NOT a bare `parent_agent_id: ""`. Clearing the
 * column alone would drop the base role's prompt out of what this agent runs
 * with, silently changing its behaviour. "Independent base role" therefore
 * routes through the existing solidify endpoint, which bakes the inherited
 * prompt into this agent's own `instructions` first and detaches second — the
 * same non-lossy unbind the archive guard offers.
 */
export function BaseRoleField({
  agent,
  agents,
  visibleChildren,
  canEdit,
  onAttach,
  onDetach,
}: {
  agent: Agent;
  /** The workspace agent list the page already holds. */
  agents: readonly Agent[];
  /** Specialisations of this agent visible to the caller; the fallback for a
   *  backend that does not serve `child_count`. */
  visibleChildren: number;
  canEdit: boolean;
  /** Writes `parent_agent_id`. Rejects (and toasts) are the caller's. */
  onAttach: (parentAgentId: string) => Promise<void>;
  /** Solidify-and-unbind: keeps the prompt, drops the link. */
  onDetach: () => Promise<void>;
}) {
  const { t } = useT("agents");
  const persisted = agent.parent_agent_id ?? NO_BASE_ROLE;
  const [value, setValue] = useState(persisted);
  const [saving, setSaving] = useState(false);
  // Re-sync when the STORED relationship changes — another tab's edit, this
  // page's own save, or a different agent mounted into the same tab. A refetch
  // that returns the same parent leaves an unsaved pick alone.
  const storedKey = `${agent.id}:${persisted}`;
  const [syncedKey, setSyncedKey] = useState(storedKey);
  if (syncedKey !== storedKey) {
    setSyncedKey(storedKey);
    setValue(persisted);
  }
  const options = useMemo(
    () => baseRoleOptionsFor(agents, agent.id),
    [agents, agent.id],
  );
  const attached = isSpecialization(agent);
  const changeable = canChangeBaseRole(agent, visibleChildren);

  // A base role that already has specialisations is at the depth limit. Say so
  // instead of rendering a picker whose every choice ends in a 400.
  if (!changeable) {
    return (
      <div
        data-testid="agent-base-role-field"
        className="rounded-lg border bg-muted/30 px-3 py-2.5"
      >
        <p className="text-body font-medium">
          {t(($) => $.specialization.base_role_label)}
        </p>
        <p className="mt-1 text-caption leading-snug text-muted-foreground">
          {t(($) => $.specialization.change_base_role_locked)}
        </p>
      </div>
    );
  }

  // Nothing to attach to and nothing attached: the picker would offer only
  // the state the agent is already in.
  if (!attached && options.length === 0) return null;

  const dirty = value !== persisted;
  const detaching = dirty && value === NO_BASE_ROLE;

  const handleSave = async () => {
    setSaving(true);
    try {
      if (detaching) {
        await onDetach();
      } else {
        await onAttach(value);
      }
    } catch {
      // The page toasts; put the field back on the stored value so it never
      // shows a relationship the server refused.
      setValue(persisted);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div
      data-testid="agent-base-role-field"
      className="rounded-lg border bg-muted/30 px-3 py-2.5"
    >
      <label
        htmlFor={`agent-base-role-${agent.id}`}
        className="text-body font-medium"
      >
        {t(($) => $.specialization.base_role_label)}
      </label>
      <p className="mt-1 text-caption leading-snug text-muted-foreground">
        {t(($) => $.specialization.change_base_role_hint)}
      </p>
      <div className="mt-2 flex flex-wrap items-start gap-2">
        <BaseRoleSelect
          id={`agent-base-role-${agent.id}`}
          agents={options}
          value={value}
          onChange={setValue}
          disabled={!canEdit || saving}
          className="min-w-56 flex-1"
        />
        {dirty && (
          <Button
            size="sm"
            data-testid="agent-base-role-save"
            disabled={!canEdit || saving}
            onClick={() => void handleSave()}
          >
            {saving && (
              <Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
            )}
            {t(($) => $.specialization.change_base_role_save)}
          </Button>
        )}
      </div>
      {detaching && (
        <p
          data-testid="agent-base-role-detach-hint"
          className="mt-2 text-caption leading-snug text-muted-foreground"
        >
          {t(($) => $.specialization.change_base_role_detach_hint)}
        </p>
      )}
    </div>
  );
}
