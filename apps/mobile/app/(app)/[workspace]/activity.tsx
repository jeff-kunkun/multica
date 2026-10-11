import { useState } from "react";
import { ScrollView, Switch, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { router } from "expo-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Text } from "@/components/ui/text";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import { ApiError } from "@/data/api";
import { fastAccessOptions } from "@/data/queries/fast-access";
import { useT } from "@/lib/i18n";

export default function Activity() {
  const { t } = useT("inbox");
  const insets = useSafeAreaInsets();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const [allVisible, setAllVisible] = useState(true);
  const [since, setSince] = useState(() => new Date(Date.now() - 30 * 60_000).toISOString());
  const query = useInfiniteQuery(fastAccessOptions(userId, wsId, allVisible, since));
  const pages = query.data?.pages ?? [];
  const last = pages.at(-1);
  // A continuation failure can mean revoked access; hide the entire stale batch.
  const runs = query.isError ? [] : pages.flatMap((p) => p.activeRuns);
  const chats = query.isError ? [] : pages.flatMap((p) => p.changes).filter((item) => item.kind === "chat");
  const phaseLabel = (phase?: string) => {
    switch (phase) {
      case "executing": return t("fast_access.executing");
      case "queued": return t("fast_access.queued");
      case "waiting": return t("fast_access.waiting");
      default: return t("fast_access.unknown");
    }
  };
  return (
    <View className="flex-1 bg-background">
      <View className="flex-row items-center justify-between gap-2 px-4 pt-4 pb-2">
        <Text className="flex-1 text-lg font-semibold">{t("fast_access.title")}</Text>
        <Button variant="ghost" className="min-h-11" onPress={() => {
          if (router.canGoBack()) router.back();
          else if (wsSlug) router.replace(`/${wsSlug}/inbox`);
        }}><Text>{t("fast_access.close")}</Text></Button>
      </View>
      <ScrollView className="flex-1" contentContainerStyle={{ padding: 16, paddingBottom: Math.max(insets.bottom, 16), gap: 16 }}>
        <View className="flex-row items-center gap-2">
          <Text className="flex-1">{t(allVisible ? "fast_access.all" : "fast_access.current")}</Text>
          <Switch value={allVisible} onValueChange={setAllVisible} accessibilityLabel={t("fast_access.all")} style={{ minHeight: 44 }} />
        </View>
        <Button variant="outline" className="min-h-11" disabled={query.isFetching}
          onPress={() => setSince(new Date(Date.now() - 30 * 60_000).toISOString())}>
          <Text>{t("fast_access.refresh")}</Text>
        </Button>
        {query.isPending && <Text>{t("fast_access.loading")}</Text>}
        {query.isError && <Text accessibilityRole="alert" className="text-destructive">{t(query.error instanceof ApiError && query.error.status === 403 ? "fast_access.denied" : "fast_access.error")}</Text>}
        {!query.isPending && !query.isError && <>
          {last && <Text className="text-sm text-muted-foreground">{t("fast_access.as_of", { time: new Date(last.asOf).toLocaleString() })}</Text>}
          <Text className="font-semibold">{t("fast_access.runs")} · {runs.length}{!last?.activeRunsComplete && "+"}</Text>
          {runs.map((run) => <View key={`${run.workspaceId}:${run.kind}:${run.id}`} className="gap-1 border-b border-border pb-3">
            {allVisible && run.workspaceName && <Text className="text-sm text-muted-foreground">{run.workspaceName}</Text>}
            <Text>{run.title || t("fast_access.untitled_run")}</Text>
            <Text className="text-sm text-muted-foreground">{phaseLabel(run.phase)}{run.sourceType === "autopilot" && ` · ${t("fast_access.autopilot")}`}</Text>
          </View>)}
          {!runs.length && last?.activeRunsComplete && <Text>{t("fast_access.no_runs")}</Text>}
          <Text className="font-semibold">{t("fast_access.chats")}</Text>
          {chats.map((chat) => <View key={`${chat.workspaceId}:${chat.kind}:${chat.id}`} className="gap-1 border-b border-border pb-3">
            {allVisible && chat.workspaceName && <Text className="text-sm text-muted-foreground">{chat.workspaceName}</Text>}
            <Text>{chat.title || t("fast_access.untitled_chat")}</Text>
            <Text className="text-sm text-muted-foreground">{t("fast_access.unread", { count: chat.unreadCount ?? 0 })}</Text>
            {chat.preview && <Text>{chat.preview}{chat.contentTruncated && "..."}</Text>}
            <Text className="text-sm text-muted-foreground">{new Date(chat.lastActiveAt ?? chat.changedAt).toLocaleString()}</Text>
          </View>)}
          {!chats.length && last?.complete && <Text>{t("fast_access.no_chats")}</Text>}
          <Text>{t(last?.complete ? "fast_access.complete" : "fast_access.partial")}</Text>
          {query.hasNextPage && <Button variant="outline" className="min-h-11" disabled={query.isFetching} onPress={() => void query.fetchNextPage()}>
            <Text>{t(query.isFetchingNextPage ? "fast_access.loading" : "fast_access.more")}</Text>
          </Button>}
        </>}
      </ScrollView>
    </View>
  );
}
