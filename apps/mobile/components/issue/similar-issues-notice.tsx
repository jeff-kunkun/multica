import { useEffect, useState } from "react";
import { Pressable, Text, View } from "react-native";
import { router } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/data/api";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export function SimilarIssuesNotice({
  title, description, projectId,
}: {
  title: string;
  description: string;
  projectId?: string;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const slug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const [debounced, setDebounced] = useState({ title, description, projectId });
  useEffect(() => {
    const timer = setTimeout(() => setDebounced({ title, description, projectId }), 450);
    return () => clearTimeout(timer);
  }, [title, description, projectId]);
  const { data } = useQuery({
    queryKey: ["issues", wsId, "similar", debounced],
    queryFn: ({ signal }) => api.similarIssues({
      project_id: debounced.projectId!,
      title: debounced.title,
      description: debounced.description,
    }, { signal }),
    enabled: !!debounced.projectId && debounced.title.trim().length >= 3,
    retry: false,
    staleTime: 10_000,
  });
  if (debounced.projectId !== projectId || debounced.title !== title || !data?.candidates.length) return null;
  return (
    <View className="rounded-lg border border-border bg-muted p-3 gap-2">
      <Text className="text-sm font-semibold text-foreground">{t("new.similar_title")}</Text>
      {data.candidates.map((candidate) => (
        <Pressable
          key={candidate.id}
          className="min-h-11 justify-center py-2"
          accessibilityRole="link"
          onPress={() => router.push(`/${slug}/issue/${candidate.id}`)}
        >
          <Text className="text-sm text-foreground">
            {candidate.identifier} · {candidate.title}
            {candidate.recent_done ? ` · ${t("new.similar_done")}` : ""}
          </Text>
        </Pressable>
      ))}
      <Text className="text-xs text-muted-foreground">{t("new.similar_hint")}</Text>
    </View>
  );
}
