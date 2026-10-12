/**
 * Project executor allowlist picker (DENE-1959) — formSheet, multi-select:
 * automatic dispatch in this project only picks the chosen agents; 不限
 * clears the list. Mirrors `packages/views/projects/components/project-executor-select.tsx`:
 * only workspace admins edit, others see the list with the reason as header.
 */
import { useLocalSearchParams } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { DomainPickerBody } from "@/components/domain/domain-picker-body";
import { useAuthStore } from "@/data/auth-store";
import { agentListOptions } from "@/data/queries/agents";
import { memberListOptions } from "@/data/queries/members";
import { projectDetailOptions } from "@/data/queries/projects";
import { useUpdateProject } from "@/data/mutations/projects";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export default function ProjectExecutorsPickerRoute() {
  const { t } = useT("projects");
  const { id } = useLocalSearchParams<{ id: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const userId = useAuthStore((s) => s.user?.id);
  const { data: project } = useQuery(projectDetailOptions(wsId, id));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const role = members.find((m) => m.user_id === userId)?.role;
  const canEdit = role === "owner" || role === "admin";
  const updateProject = useUpdateProject(id);
  const selected = project?.executor_agent_ids ?? [];

  return (
    <DomainPickerBody
      title={t("properties.executors")}
      header={canEdit ? t("properties.executors_header") : t("properties.executors_admin_only")}
      options={[
        { id: "", name: t("properties.executors_any") },
        ...agents
          .filter((a) => !a.archived_at || selected.includes(a.id))
          .map((a) => ({ id: a.id, name: a.name })),
      ]}
      selected={selected}
      onPick={(picked) => {
        if (!canEdit) return;
        const next = !picked
          ? []
          : selected.includes(picked)
            ? selected.filter((s) => s !== picked)
            : [...selected, picked];
        updateProject.mutate({ executor_agent_ids: next });
      }}
    />
  );
}
