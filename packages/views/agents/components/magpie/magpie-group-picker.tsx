"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAgentPermissions } from "@multica/core/permissions";
import {
  agentMagpieGroups,
  withAgentMagpieGroups,
  type MagpieRoute,
} from "@multica/core/runtimes";
import { cacheAgentResponse } from "@multica/core/workspace/queries";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

/**
 * The magpie groups an agent can run on, its default first. Each issue picks
 * one of them for its runs; an issue that picks none runs on the default.
 * With none ticked the agent runs on its CLI's own group. Callers render it
 * only for CLIs the office can pin (`canPinMagpieGroup`) on machines that list
 * groups.
 */
export function MagpieGroupPicker({
  agent,
  route,
}: {
  agent: Agent;
  route: MagpieRoute;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { canEdit } = useAgentPermissions(agent, wsId);
  const [saving, setSaving] = useState(false);

  const listed = agentMagpieGroups(agent.runtime_config);
  const names = route.groups.map((g) => g.name);
  // A listed group the machine no longer has stays visible so it can be removed.
  for (const name of listed) if (!names.includes(name)) names.push(name);
  const disabled = !canEdit.allowed || saving;

  async function persist(next: string[]) {
    if (saving) return;
    setSaving(true);
    try {
      const updated = await api.updateAgent(agent.id, {
        runtime_config: withAgentMagpieGroups(agent.runtime_config, next),
      });
      cacheAgentResponse(qc, wsId, updated);
    } catch (e) {
      toast.error(t(($) => $.magpie.save_failed), {
        description: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  }

  return (
    <ul className="space-y-1" aria-busy={saving || undefined}>
      {names.map((name) => {
        const checked = listed.includes(name);
        const isDefault = listed[0] === name;
        return (
          <li key={name} className="flex min-h-6 items-center gap-2">
            <label className="flex min-w-0 flex-1 items-center gap-2">
              <Checkbox
                checked={checked}
                disabled={disabled}
                onCheckedChange={(on) =>
                  void persist(on ? [...listed, name] : listed.filter((g) => g !== name))
                }
              />
              <span className="truncate text-foreground">{name}</span>
            </label>
            {isDefault ? (
              <span className="shrink-0 text-muted-foreground">{t(($) => $.magpie.default)}</span>
            ) : (
              checked && (
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  disabled={disabled}
                  onClick={() => void persist([name, ...listed.filter((g) => g !== name)])}
                >
                  {t(($) => $.magpie.make_default)}
                </Button>
              )
            )}
          </li>
        );
      })}
    </ul>
  );
}
