import { useAuthStore } from "@/data/auth-store";
import { ActivityIndicator, Pressable, ScrollView, View } from "react-native";
import { router, useLocalSearchParams } from "expo-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ChatLinkedProjectRef } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { api } from "@/data/api";
import { chatKeys } from "@/data/queries/chat";
import { referenceProjectOptions } from "@/data/queries/projects";
import { chatReferenceOptions } from "@/data/queries/workspace-links";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useChatReferencesDraftStore } from "@/data/stores/chat-references-draft-store";
import { useT } from "@/lib/i18n";

const key = (ref: ChatLinkedProjectRef) => `${ref.link_id}/${ref.project_id}`;
export default function ChatReferences() {
  const { sessionId = "" } = useLocalSearchParams<{ sessionId?: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("projects");
  const { t: tw } = useT("workspace");
  const qc = useQueryClient();
  const userId = useAuthStore((s) => s.user?.id);
  const options = useQuery(referenceProjectOptions(wsId));
  const queryOptions = chatReferenceOptions(wsId, sessionId);
  const session = useQuery(queryOptions);
  const canWrite =
    !sessionId || (!!userId && session.data?.creator_id === userId);
  const draft = useChatReferencesDraftStore();
  const selected = sessionId
    ? (session.data?.linked_projects ?? [])
    : draft.workspaceId === wsId
      ? draft.refs
      : [];
  const rows = (options.data?.projects ?? []).map((p) => ({
    link_id: p.link_id,
    project_id: p.id,
    title: p.title,
    source_name: p.source.name,
    available: true,
  }));
  for (const ref of session.data?.linked_projects ?? [])
    if (!rows.some((p) => key(p) === key(ref)))
      rows.push({ ...ref, available: false });
  for (const ref of selected)
    if (!rows.some((p) => key(p) === key(ref)))
      rows.push({
        ...ref,
        title: t("references.unavailable"),
        source_name: "",
        available: false,
      });
  const save = useMutation({
    mutationFn: (refs: ChatLinkedProjectRef[]) =>
      api.setChatReferences(sessionId, refs),
    onSuccess: (result) => {
      qc.setQueryData(queryOptions.queryKey, result);
    },
    onSettled: () =>
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) }),
  });
  const loading = options.isPending || (!!sessionId && session.isPending);
  const failed = options.isError || (!!sessionId && session.isError);
  const change = (refs: ChatLinkedProjectRef[]) => {
    if (sessionId) save.mutate(refs);
    else if (wsId) draft.set(wsId, refs);
  };
  return (
    <View className="flex-1 bg-background">
      <View className="flex-row items-center gap-2 px-4 pt-3">
        <Text className="flex-1 text-lg font-semibold">
          {t("references.title")}
        </Text>
        <Pressable
          accessibilityRole="button"
          className="min-h-11 justify-center px-2"
          onPress={() => router.back()}
        >
          <Text>{tw("links.mobile.close")}</Text>
        </Pressable>
      </View>
      <ScrollView
        contentInsetAdjustmentBehavior="automatic"
        contentContainerClassName="px-4 pb-10 gap-2"
      >
        <Text className="text-sm text-muted-foreground">
          {t("references.description")}
        </Text>
        {loading ? (
          <ActivityIndicator />
        ) : failed ? (
          <Pressable
            accessibilityRole="button"
            className="min-h-11 justify-center"
            onPress={() => {
              void options.refetch();
              if (sessionId) void session.refetch();
            }}
          >
            <Text>
              {t("references.load_error")} · {t("references.retry")}
            </Text>
          </Pressable>
        ) : (
          <>
            {!rows.length && (
              <Text className="py-4 text-muted-foreground">
                {t("references.empty")}
              </Text>
            )}
            {rows.map((row) => {
              const checked = selected.some((ref) => key(ref) === key(row));
              const unavailable = !row.available && !checked;
              return (
                <Pressable
                  key={key(row)}
                  accessibilityRole="checkbox"
                  accessibilityState={{
                    checked,
                    disabled: !canWrite || save.isPending || unavailable,
                  }}
                  disabled={!canWrite || save.isPending || unavailable}
                  className="min-h-11 flex-row items-center gap-3 border-b border-border py-3"
                  onPress={() =>
                    change(
                      checked
                        ? selected
                            .filter((p) => key(p) !== key(row))
                            .map(({ link_id, project_id }) => ({
                              link_id,
                              project_id,
                            }))
                        : [
                            ...selected.map(({ link_id, project_id }) => ({
                              link_id,
                              project_id,
                            })),
                            {
                              link_id: row.link_id,
                              project_id: row.project_id,
                            },
                          ],
                    )
                  }
                >
                  <Text>{checked ? "✓" : "○"}</Text>
                  <View className="flex-1">
                    <Text>{row.title}</Text>
                    <Text className="text-sm text-muted-foreground">
                      {row.available
                        ? row.source_name
                        : t("references.unavailable")}
                    </Text>
                  </View>
                </Pressable>
              );
            })}
          </>
        )}
        {!canWrite && !loading && (
          <Text className="text-muted-foreground">
            {tw("links.mobile.chat_creator")}
          </Text>
        )}
        {save.isPending && (
          <Text className="text-muted-foreground">
            {tw("links.mobile.saving")}
          </Text>
        )}
        {save.isError && (
          <Text accessibilityRole="alert" className="text-destructive">
            {t("references.save_error")} · {save.error.message}
          </Text>
        )}
      </ScrollView>
    </View>
  );
}
