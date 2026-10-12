import {
  ActivityIndicator,
  Pressable,
  RefreshControl,
  ScrollView,
  View,
} from "react-native";
import { Stack, router, useLocalSearchParams } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { Text } from "@/components/ui/text";
import { LinkRow } from "@/components/workspace-links/link-row";
import { workspaceLinksOptions } from "@/data/queries/workspace-links";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export default function WorkspaceLinks() {
  const { workspace } = useLocalSearchParams<{ workspace: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { t } = useT("workspace");
  const query = useQuery(workspaceLinksOptions(wsId));
  return (
    <ScrollView
      className="flex-1 bg-background"
      contentInsetAdjustmentBehavior="automatic"
      contentContainerClassName="px-4 pb-10"
      refreshControl={
        <RefreshControl
          refreshing={query.isRefetching}
          onRefresh={() => void query.refetch()}
        />
      }
    >
      <Stack.Screen options={{ title: t("links.tab.title") }} />
      {query.isPending ? (
        <ActivityIndicator />
      ) : query.isError ? (
        <Pressable
          accessibilityRole="button"
          className="min-h-11 justify-center"
          onPress={() => void query.refetch()}
        >
          <Text>{t("links.mobile.load_error")}</Text>
        </Pressable>
      ) : (
        (["viewer", "source"] as const).map((side) => {
          const links = query.data.links.filter((link) => link.side === side);
          const allowed =
            side === "source" ? query.data.can.create : query.data.can.pull;
          return (
            <View key={side} className="pt-5 gap-1">
              <Text className="text-lg font-semibold">
                {t(
                  side === "source"
                    ? "links.tab.outgoing_title"
                    : "links.tab.incoming_title",
                )}
              </Text>
              <Text className="text-sm text-muted-foreground">
                {t(
                  side === "source"
                    ? "links.tab.outgoing_description"
                    : "links.tab.incoming_description",
                )}
              </Text>
              {links.map((link) => (
                <LinkRow
                  key={link.id}
                  link={link}
                  can={query.data.can}
                  wsId={wsId!}
                  slug={workspace}
                />
              ))}
              {!links.length && (
                <Text className="py-3 text-muted-foreground">
                  {t(
                    side === "source"
                      ? "links.tab.outgoing_empty"
                      : "links.tab.incoming_empty",
                  )}
                </Text>
              )}
              <Pressable
                accessibilityRole="button"
                disabled={!allowed}
                className="min-h-11 justify-center"
                onPress={() =>
                  router.push({
                    pathname: "/[workspace]/workspace-links/projects",
                    params: {
                      workspace,
                      direction: side === "source" ? "offer" : "pull",
                    },
                  })
                }
              >
                <Text
                  className={allowed ? "text-primary" : "text-muted-foreground"}
                >
                  {t(
                    side === "source"
                      ? "links.mobile.offer"
                      : "links.mobile.pull",
                  )}
                </Text>
              </Pressable>
              {!allowed && (
                <Text className="text-sm text-muted-foreground">
                  {t(
                    side === "source"
                      ? "links.tab.create_reason"
                      : "links.tab.pull_reason",
                  )}
                </Text>
              )}
            </View>
          );
        })
      )}
    </ScrollView>
  );
}
