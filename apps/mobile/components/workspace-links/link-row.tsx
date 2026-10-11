import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Alert, Pressable, Switch, View } from "react-native";
import { router } from "expo-router";
import type {
  WorkspaceLink,
  WorkspaceLinkAbilities,
} from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { api } from "@/data/api";
import { refreshWorkspaceLinks } from "@/data/realtime/workspace-link-ws-updaters";
import { useT } from "@/lib/i18n";

export function LinkRow({
  link,
  can,
  wsId,
  slug,
}: {
  link: WorkspaceLink;
  can: WorkspaceLinkAbilities;
  wsId: string;
  slug: string;
}) {
  const { t } = useT("workspace");
  const qc = useQueryClient();
  const save = useMutation({
    mutationFn: async (
      body: { managed: boolean } | { accept: true } | "revoke",
    ) => {
      if (body === "revoke") await api.revokeWorkspaceLink(link.id);
      else await api.writeWorkspaceLink(link.id, body);
    },
    onSettled: () => refreshWorkspaceLinks(qc, wsId),
  });
  const peer = link.side === "source" ? link.target : link.source;
  const canChange = link.side === "source" ? can.create : can.accept;
  const revokeLabel =
    link.side === "viewer" && link.status === "pending"
      ? t("links.tab.decline")
      : t("links.tab.revoke");
  return (
    <View className="gap-2 border-b border-border py-4">
      <Text className="font-semibold">{peer.name}</Text>
      <Text className="text-sm text-muted-foreground">
        {peer.slug} ·{" "}
        {t(
          link.status === "active"
            ? "links.tab.status_active"
            : "links.tab.status_pending",
        )}
      </Text>
      <Text className="text-sm">
        {t("links.tab.sharing", {
          projects: link.projects.map((p) => p.title).join("、") || "—",
        })}
      </Text>
      <Pressable
        accessibilityRole="button"
        disabled={!link.can_update_projects || save.isPending}
        accessibilityState={{
          disabled: !link.can_update_projects || save.isPending,
        }}
        className="min-h-11 justify-center"
        onPress={() =>
          router.push({
            pathname: "/[workspace]/workspace-links/projects",
            params: { workspace: slug, linkId: link.id },
          })
        }
      >
        <Text
          className={
            !link.can_update_projects ? "text-muted-foreground" : "text-primary"
          }
        >
          {t("links.tab.change_projects")}
        </Text>
      </Pressable>
      {!link.can_update_projects && (
        <Text className="text-sm text-muted-foreground">
          {t("links.tab.projects_reason")}
        </Text>
      )}
      <View className="min-h-11 flex-row items-center gap-3">
        <View className="flex-1 gap-1">
          <Text>{t("links.tab.managed_label")}</Text>
          <Text className="text-sm text-muted-foreground">
            {t(
              !link.can_set_managed
                ? link.status === "pending" && !link.managed
                  ? "links.tab.managed_pending"
                  : "links.tab.managed_reason"
                : link.side === "source"
                  ? "links.tab.managed_hint_outgoing"
                  : "links.tab.managed_hint_incoming",
            )}
          </Text>
        </View>
        <View className="min-h-11 justify-center">
          <Switch
            accessibilityLabel={t("links.tab.managed_label")}
            value={!!link.managed}
            disabled={!link.can_set_managed || save.isPending}
            onValueChange={(managed) => save.mutate({ managed })}
          />
        </View>
      </View>
      {link.side === "viewer" && link.status === "active" && (
        <Pressable
          accessibilityRole="button"
          className="min-h-11 justify-center"
          onPress={() =>
            router.push({
              pathname: "/[workspace]/workspace-links/[id]",
              params: { workspace: slug, id: link.id },
            })
          }
        >
          <Text className="text-primary">{t("links.mobile.read")}</Text>
        </Pressable>
      )}
      <View className="flex-row flex-wrap gap-5">
        {link.side === "viewer" && link.status === "pending" && (
          <Pressable
            accessibilityRole="button"
            className="min-h-11 justify-center"
            disabled={!can.accept || save.isPending}
            onPress={() => save.mutate({ accept: true })}
          >
            <Text
              className={!can.accept ? "text-muted-foreground" : "text-primary"}
            >
              {t("links.tab.accept")}
            </Text>
          </Pressable>
        )}
        <Pressable
          accessibilityRole="button"
          className="min-h-11 justify-center"
          disabled={!canChange || save.isPending}
          onPress={() =>
            Alert.alert(
              revokeLabel,
              t("links.tab.revoke_description", { name: peer.name }),
              [
                { text: t("links.tab.cancel"), style: "cancel" },
                {
                  text: revokeLabel,
                  style: "destructive",
                  onPress: () => save.mutate("revoke"),
                },
              ],
            )
          }
        >
          <Text
            className={canChange ? "text-destructive" : "text-muted-foreground"}
          >
            {revokeLabel}
          </Text>
        </Pressable>
      </View>
      {!canChange && (
        <Text className="text-sm text-muted-foreground">
          {t(
            link.side === "source"
              ? "links.tab.create_reason"
              : "links.tab.accept_reason",
          )}
        </Text>
      )}
      {save.isPending && (
        <Text className="text-sm text-muted-foreground">
          {t("links.mobile.saving")}
        </Text>
      )}
      {save.isError && (
        <Text accessibilityRole="alert" className="text-destructive">
          {t("links.tab.failed")}
          {save.error instanceof Error ? ` · ${save.error.message}` : ""}
        </Text>
      )}
    </View>
  );
}
