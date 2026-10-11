import { useEffect, useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { router, useLocalSearchParams } from "expo-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Text } from "@/components/ui/text";
import { api } from "@/data/api";
import { projectListOptions } from "@/data/queries/projects";
import {
  workspaceLinkLookupOptions,
  workspaceLinksOptions,
} from "@/data/queries/workspace-links";
import { refreshWorkspaceLinks } from "@/data/realtime/workspace-link-ws-updaters";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export default function LinkProjects() {
  const { linkId, direction } = useLocalSearchParams<{
    linkId?: string;
    direction?: string;
  }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("workspace");
  const qc = useQueryClient();
  const links = useQuery(workspaceLinksOptions(wsId));
  const link = links.data?.links.find((item) => item.id === linkId);
  const pull = link ? link.side === "viewer" : direction === "pull";
  const allowed = linkId
    ? !!link?.can_update_projects
    : !!(pull ? links.data?.can.pull : links.data?.can.create);
  const [address, setAddress] = useState("");
  const [target, setTarget] = useState("");
  const [selected, setSelected] = useState<string[] | null>(null);
  useEffect(() => {
    const timer = setTimeout(() => setTarget(address.trim()), 350);
    return () => clearTimeout(timer);
  }, [address]);
  const lookup = useQuery(
    workspaceLinkLookupOptions(
      wsId,
      link?.source.slug ?? target,
      allowed && (pull || !linkId),
    ),
  );
  const local = useQuery({
    ...projectListOptions(wsId),
    enabled: !!wsId && allowed && !pull,
  });
  const projects = pull
    ? (lookup.data?.pull?.projects ?? [])
    : (local.data ?? []).filter(
        (p) => (p.visibility ?? "private") !== "private",
      );
  const picked = selected ?? link?.projects.map((p) => p.id) ?? [];
  const valid = picked.filter((id) => projects.some((p) => p.id === id));
  const ready =
    allowed &&
    valid.length > 0 &&
    (pull ? !!lookup.data?.pull?.allowed : true) &&
    (!!linkId || (!!lookup.data?.workspace && target === address.trim()));
  const save = useMutation({
    mutationFn: () =>
      linkId
        ? api.writeWorkspaceLink(linkId, { project_ids: valid })
        : api.writeWorkspaceLink(null, {
            direction: pull ? "pull" : "offer",
            target_slug: lookup.data!.workspace.slug,
            project_ids: valid,
          }),
    onSuccess: async () => {
      await refreshWorkspaceLinks(qc, wsId!);
      router.back();
    },
    onError: () => {
      void refreshWorkspaceLinks(qc, wsId!);
    },
  });
  return (
    <View className="flex-1 bg-background">
      <View className="flex-row items-center justify-between px-4 pt-3">
        <Text className="flex-1 text-lg font-semibold">
          {t("links.tab.projects_label")}
        </Text>
        <Pressable
          accessibilityRole="button"
          className="min-h-11 justify-center px-2"
          disabled={save.isPending}
          onPress={() => router.back()}
        >
          <Text>{t("links.tab.cancel")}</Text>
        </Pressable>
      </View>
      <ScrollView
        automaticallyAdjustKeyboardInsets
        keyboardShouldPersistTaps="handled"
        contentInsetAdjustmentBehavior="automatic"
        contentContainerClassName="px-4 pb-10 gap-3"
      >
        <Text className="text-sm text-muted-foreground">{t("links.mobile.context_shared")}</Text>
        {links.isPending && <ActivityIndicator />}
        {links.isError && (
          <Pressable
            accessibilityRole="button"
            className="min-h-11 justify-center"
            onPress={() => void links.refetch()}
          >
            <Text>{t("links.mobile.load_error")}</Text>
          </Pressable>
        )}
        {!linkId && (
          <>
            <Text>
              {t(
                pull ? "links.tab.pull_target_label" : "links.tab.target_label",
              )}
            </Text>
            <TextInput
              accessibilityLabel={t("links.tab.target_label")}
              placeholder={t("links.tab.target_placeholder")}
              value={address}
              editable={allowed && !save.isPending}
              onChangeText={(value) => {
                setAddress(value);
                setSelected([]);
              }}
              autoCapitalize="none"
              autoCorrect={false}
              className="min-h-11 rounded-md border border-border px-3 text-foreground"
            />
          </>
        )}
        {link && <Text>{pull ? link.source.name : link.target.name}</Text>}
        {!linkId && lookup.data && <Text>{lookup.data.workspace.name}</Text>}
        {lookup.isFetching || local.isFetching ? <ActivityIndicator /> : null}
        {(lookup.isError || local.isError) && (
          <Pressable
            accessibilityRole="button"
            className="min-h-11 justify-center"
            onPress={() => {
              if (pull || !linkId) void lookup.refetch();
              if (!pull) void local.refetch();
            }}
          >
            <Text>{t("links.mobile.load_error")}</Text>
          </Pressable>
        )}
        {pull && lookup.data && !lookup.data.pull?.allowed && (
          <Text className="text-muted-foreground">
            {t("links.tab.pull_need_owner")}
          </Text>
        )}
        {!allowed && !links.isPending && (
          <Text className="text-muted-foreground">
            {t(
              linkId
                ? "links.tab.projects_reason"
                : pull
                  ? "links.tab.pull_reason"
                  : "links.tab.create_reason",
            )}
          </Text>
        )}
        {projects.map((project) => {
          const checked = picked.includes(project.id);
          return (
            <Pressable
              key={project.id}
              accessibilityRole="checkbox"
              accessibilityState={{
                checked,
                disabled: !allowed || save.isPending,
              }}
              disabled={!allowed || save.isPending}
              className="min-h-11 flex-row items-center gap-3 border-b border-border py-3"
              onPress={() =>
                setSelected(
                  checked
                    ? picked.filter((id) => id !== project.id)
                    : [...picked, project.id],
                )
              }
            >
              <Text>{checked ? "✓" : "○"}</Text>
              <Text className="flex-1">{project.title}</Text>
            </Pressable>
          );
        })}
        {!projects.length && !lookup.isFetching && !local.isFetching && (
          <Text className="text-muted-foreground">
            {t("links.tab.no_shareable_projects")}
          </Text>
        )}
        <Pressable
          accessibilityRole="button"
          disabled={!ready || save.isPending}
          className="min-h-11 justify-center"
          onPress={() => save.mutate()}
        >
          <Text className={ready ? "text-primary" : "text-muted-foreground"}>
            {t(save.isPending ? "links.mobile.saving" : "links.tab.save")}
          </Text>
        </Pressable>
        {save.isError && (
          <Text accessibilityRole="alert" className="text-destructive">
            {t("links.tab.failed")} · {save.error.message}
          </Text>
        )}
      </ScrollView>
    </View>
  );
}
