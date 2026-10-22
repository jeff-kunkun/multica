"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Issue } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueKeys } from "@multica/core/issues/queries";
import {
  agentMagpieGroups,
  canPinMagpieGroup,
  ISSUE_MAGPIE_GROUP_KEY,
  runtimeListOptions,
} from "@multica/core/runtimes";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useT } from "../../../i18n";
import { useWorkspaceMagpieEnabled } from "../../../workspace/use-magpie-mode";
import { PropertyPicker, PickerItem } from "./property-picker";

/**
 * The magpie groups an issue's agent assignee can run on, when it has more
 * than one. `null` otherwise, so the sidebar shows no row.
 */
export function useIssueMagpieGroups(issue: Pick<Issue, "assignee_type" | "assignee_id">): string[] | null {
  const wsId = useWorkspaceId();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const magpie = useWorkspaceMagpieEnabled();
  if (!magpie || issue.assignee_type !== "agent" || !issue.assignee_id) return null;
  const agent = agents.find((a) => a.id === issue.assignee_id);
  const runtime = agent ? runtimes.find((r) => r.id === agent.runtime_id) : undefined;
  if (!agent || !canPinMagpieGroup(runtime?.provider)) return null;
  const groups = agentMagpieGroups(agent.runtime_config);
  return groups.length > 1 ? groups : null;
}

/**
 * Which of the assignee's magpie groups this issue's runs use, kept in the
 * issue's `magpie_group` metadata. Picking the default clears the key, so a
 * later change of the agent's default carries over.
 */
export function MagpieGroupPicker({ issue, groups }: { issue: Issue; groups: string[] }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);

  const chosen = issue.metadata?.[ISSUE_MAGPIE_GROUP_KEY];
  // A choice the agent no longer lists runs on the default, so it reads as one.
  const current = typeof chosen === "string" && groups.includes(chosen) ? chosen : groups[0]!;

  async function choose(group: string) {
    setOpen(false);
    if (saving || group === current) return;
    setSaving(true);
    try {
      if (group === groups[0]) await api.deleteIssueMetadataKey(issue.id, ISSUE_MAGPIE_GROUP_KEY);
      else await api.setIssueMetadataKey(issue.id, ISSUE_MAGPIE_GROUP_KEY, group);
      await qc.invalidateQueries({ queryKey: issueKeys.detail(wsId, issue.id) });
    } catch (e) {
      toast.error(t(($) => $.detail.magpie_group_save_failed), {
        description: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  }

  const label = (group: string) =>
    group === groups[0] ? t(($) => $.detail.magpie_group_default, { group }) : group;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-56"
      align="start"
      trigger={<span className="truncate">{label(current)}</span>}
    >
      {groups.map((group) => (
        <PickerItem key={group} selected={group === current} disabled={saving} onClick={() => void choose(group)}>
          <span className="truncate">{label(group)}</span>
        </PickerItem>
      ))}
    </PropertyPicker>
  );
}
