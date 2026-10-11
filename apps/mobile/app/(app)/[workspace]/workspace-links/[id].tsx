import { useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  RefreshControl,
  ScrollView,
  View,
} from "react-native";
import { Stack, useLocalSearchParams } from "expo-router";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Text } from "@/components/ui/text";
import {
  linkedViewOptions,
  workspaceLinksOptions,
} from "@/data/queries/workspace-links";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT, useTimeAgo } from "@/lib/i18n";

export default function SharedProjects() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("workspace");
  const timeAgo = useTimeAgo();
  const [projectId, setProjectId] = useState("");
  const links = useQuery(workspaceLinksOptions(wsId));
  const managed = links.data?.links.find((link) => link.id === id)?.managed;
  const query = useInfiniteQuery(linkedViewOptions(wsId, id, projectId));
  const first = query.data?.pages[0];
  return (
    <ScrollView
      className="flex-1 bg-background"
      contentInsetAdjustmentBehavior="automatic"
      contentContainerClassName="px-4 pb-10 gap-4"
      refreshControl={
        <RefreshControl
          refreshing={query.isRefetching}
          onRefresh={() => void query.refetch()}
        />
      }
    >
      <Stack.Screen
        options={{ title: first?.source.name ?? t("links.panel.title") }}
      />
      {query.isPending ? (
        <ActivityIndicator />
      ) : query.isError ? (
        <View className="py-4 gap-2">
          <Text>{t("links.panel.gone_title")}</Text>
          <Pressable
            accessibilityRole="button"
            className="min-h-11 justify-center"
            onPress={() => void query.refetch()}
          >
            <Text>{t("links.mobile.load_error")}</Text>
          </Pressable>
        </View>
      ) : (
        <>
          <Text className="pt-4 text-muted-foreground">
            {t(
              managed
                ? "links.panel.managed_from"
                : "links.panel.read_only_from",
              { name: first?.source.name },
            )}
          </Text>
          {first?.projects.map((project) => (
            <Pressable
              key={project.id}
              accessibilityRole="button"
              accessibilityState={{ selected: projectId === project.id }}
              className="min-h-11 gap-2 border-b border-border py-4"
              onPress={() =>
                setProjectId(projectId === project.id ? "" : project.id)
              }
            >
              <Text
                className={
                  projectId === project.id
                    ? "font-semibold text-primary"
                    : "font-semibold"
                }
              >
                {project.icon} {project.title}
              </Text>
              <Text className="text-sm text-muted-foreground">
                {project.status} ·{" "}
                {t("links.panel.project_progress", {
                  done: project.done,
                  total: project.total,
                })}
              </Text>
              {!!project.description && <Text>{project.description}</Text>}
              {project.resources?.map((resource, index) => (
                <Text key={index} className="text-sm text-muted-foreground">
                  {resource.label || resource.type} ·{" "}
                  {resource.url || resource.path}
                </Text>
              ))}
              {!!project.memory_line && (
                <Text className="text-sm text-muted-foreground">
                  {project.memory_line}
                </Text>
              )}
            </Pressable>
          ))}
          <View className="min-h-11 flex-row items-center justify-between gap-3">
            <Text className="font-semibold">{t("links.panel.all_tasks")}</Text>
            {!!projectId && (
              <Pressable
                accessibilityRole="button"
                className="min-h-11 justify-center"
                onPress={() => setProjectId("")}
              >
                <Text className="text-primary">
                  {t("links.panel.show_all")}
                </Text>
              </Pressable>
            )}
          </View>
          {query.data?.pages
            .flatMap((page) => page.issues)
            .map((issue) => (
              <View
                key={issue.identifier}
                className="gap-1 border-b border-border py-3"
              >
                <Text className="text-sm text-muted-foreground">
                  {issue.identifier}
                </Text>
                <Text>{issue.title}</Text>
                <Text className="text-sm text-muted-foreground">
                  {first?.statuses.find((s) => s.key === issue.status)?.name ??
                    issue.status}{" "}
                  · {issue.assignee_name ?? "—"} · {timeAgo(issue.updated_at)}
                </Text>
              </View>
            ))}
          {!first?.issues.length && (
            <Text className="text-muted-foreground">
              {t("links.panel.no_tasks")}
            </Text>
          )}
          {query.hasNextPage && (
            <Pressable
              accessibilityRole="button"
              disabled={query.isFetchingNextPage}
              className="min-h-11 justify-center"
              onPress={() => void query.fetchNextPage()}
            >
              <Text>{t("links.panel.load_more")}</Text>
            </Pressable>
          )}
        </>
      )}
    </ScrollView>
  );
}
