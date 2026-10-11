import { Pressable, View } from "react-native";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ChatLinkedProjectRef } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { useAuthStore } from "@/data/auth-store";
import { memberListOptions } from "@/data/queries/members";
import { api } from "@/data/api";
import { projectReferencesOptions, referenceProjectOptions } from "@/data/queries/projects";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export function ProjectReferencesSetting({ projectId }: { projectId: string }) {
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("projects");
  const userId = useAuthStore((state) => state.user?.id);
  const members = useQuery(memberListOptions(wsId));
  const role = members.data?.find((member) => member.user_id === userId)?.role;
  const canWrite = role === "owner" || role === "admin" || role === "member";
  const qc = useQueryClient();
  const queryOptions = projectReferencesOptions(wsId, projectId);
  const settings = useQuery(queryOptions);
  const options = useQuery(referenceProjectOptions(wsId));
  const save = useMutation({
    mutationFn: (refs: ChatLinkedProjectRef[]) => api.setProjectReferences(projectId, refs),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryOptions.queryKey }),
  });
  const selected = settings.data?.effective_linked_projects ?? [];
  const rows = (options.data?.projects ?? []).map((p) => ({ link_id: p.link_id, project_id: p.id, title: p.title, source_name: p.source.name, available: true }));
  const key = (p: ChatLinkedProjectRef) => `${p.link_id}/${p.project_id}`;
  for (const item of selected) if (!rows.some((p) => key(p) === key(item))) rows.push({ ...item, available: false });
  return <View className="gap-2">
    <Text className="text-sm font-medium">{t("references.title")}</Text>
    <Text className="text-sm text-muted-foreground">{t("references.description")}</Text>
    {settings.isPending || options.isPending ? <Text>{t("references.loading")}</Text> : settings.isError || options.isError ? <Pressable accessibilityRole="button" className="min-h-11 justify-center" onPress={() => { void settings.refetch(); void options.refetch(); }}><Text>{t("references.load_error")} · {t("references.retry")}</Text></Pressable> : <>
      {rows.length === 0 && <Text className="text-muted-foreground">{t("references.empty")}</Text>}
      {rows.map((row) => {
        const checked = selected.some((p) => key(p) === key(row));
        return <Pressable key={key(row)} accessibilityRole="checkbox" accessibilityState={{ checked, disabled: save.isPending || !canWrite }} disabled={save.isPending || !canWrite} className="min-h-11 flex-row items-center gap-2 py-2" onPress={() => save.mutate(checked ? selected.filter((p) => key(p) !== key(row)).map(({ link_id, project_id }) => ({ link_id, project_id })) : [...selected.map(({ link_id, project_id }) => ({ link_id, project_id })), { link_id: row.link_id, project_id: row.project_id }])}>
          <Text>{checked ? "✓" : "○"}</Text><View className="flex-1"><Text>{row.title}</Text><Text className="text-sm text-muted-foreground">{row.available ? row.source_name : t("references.unavailable")}</Text></View>
        </Pressable>;
      })}
    </>}
    {!canWrite && !members.isPending && <Text className="text-muted-foreground">{t("references.read_only")}</Text>}
    {save.isError && <Text accessibilityRole="alert" className="text-destructive">{t("references.save_error")}</Text>}
  </View>;
}
